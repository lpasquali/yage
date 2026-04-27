// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	billing "cloud.google.com/go/billing/apiv1"
	"cloud.google.com/go/billing/apiv1/billingpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCP Cloud Billing Catalog API — needs an API key.
// Set GOOGLE_BILLING_API_KEY (or YAGE_GCP_API_KEY) to
// enable; otherwise this fetcher returns ErrUnavailable and
// the cost path surfaces "GCP estimate unavailable".
//
// Uses the official cloud.google.com/go/billing/apiv1 SDK with a
// REST transport (NewCloudCatalogRESTClient). option.WithAPIKey adds
// the API key to every request; the underlying transport starts from
// a clone of http.DefaultTransport at client-creation time, so
// airgap.Apply() must be called before the first GCP pricing call
// (which is always true in the normal bootstrap flow).
//
// Service IDs:
//   gcpComputeEngineService = "6F81-5844-456A"  — Compute Engine
//   gcpStorageService       = "95FF-2EF5-5EA1"  — Cloud Storage

const (
	gcpComputeEngineService = "6F81-5844-456A"
	gcpStorageService       = "95FF-2EF5-5EA1"
)

type gcpFetcher struct{}

func init() {
	Register("gcp", &gcpFetcher{})
}

// gcpAPIKey returns the configured Google Cloud Billing Catalog
// key. Read order: cfg.Cost.Credentials (set by main from
// config.Load) → env-var fallback for cases where the orchestrator
// hasn't called SetCredentials yet (e.g. test setups, --xapiri).
func gcpAPIKey() string {
	if creds.GCPAPIKey != "" {
		return creds.GCPAPIKey
	}
	if k := os.Getenv("YAGE_GCP_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("GOOGLE_BILLING_API_KEY")
}

// gcpUsdFromTier extracts the first non-zero on-demand USD price from
// a PricingInfo's tiered rates. units is an int64 string and nanos is
// int32 (e.g. units=0, nanos=23000000 → $0.023).
func gcpUsdFromTier(p *billingpb.PricingInfo) float64 {
	if p == nil || p.PricingExpression == nil {
		return 0
	}
	for _, t := range p.PricingExpression.TieredRates {
		if t.UnitPrice == nil {
			continue
		}
		usd := float64(t.UnitPrice.Units) + float64(t.UnitPrice.Nanos)/1e9
		if usd > 0 {
			return usd
		}
	}
	return 0
}

func (g *gcpFetcher) Fetch(sku, region string) (Item, error) {
	key := gcpAPIKey()
	if key == "" {
		return Item{}, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY (or YAGE_GCP_API_KEY)")
	}

	// SKU forms:
	//   "<machineType>"        e.g. "n2-standard-2", "e2-medium"
	//   "pd:balanced"          PD Balanced GB-month
	//   "pd:ssd"               PD SSD GB-month
	//   "pd:standard"          PD Standard GB-month
	if strings.HasPrefix(sku, "pd:") {
		return g.fetchPD(strings.TrimPrefix(sku, "pd:"), region, key)
	}
	return g.fetchCompute(sku, region, key)
}

func (g *gcpFetcher) fetchCompute(machineType, region, key string) (Item, error) {
	// Compute pricing in GCP is split into core (CPU) + RAM (GB)
	// SKUs per machine family. For the headline number we want
	// the explicit "<MachineType> Instance Core" + "RAM" pair.
	// As a simpler conservative path, we sum core + ram for the
	// machine family. machineType like "n2-standard-2" parses to
	// family=n2, predefined=standard, vCPU=2, RAM via convention.
	family, vcpu, ramGB, err := parseGCPMachineType(machineType)
	if err != nil {
		return Item{}, err
	}
	corePrice, err := g.findCoreRam(family, region, key, true /*core*/)
	if err != nil {
		return Item{}, err
	}
	ramPrice, err := g.findCoreRam(family, region, key, false /*ram*/)
	if err != nil {
		return Item{}, err
	}
	hourly := corePrice*float64(vcpu) + ramPrice*ramGB
	if hourly <= 0 {
		return Item{}, fmt.Errorf("gcp: zero price for %s in %s", machineType, region)
	}
	return Item{
		USDPerHour:  hourly,
		USDPerMonth: hourly * MonthlyHours,
		FetchedAt:   time.Now(),
	}, nil
}

// findCoreRam searches Compute Engine SKUs for a single
// "Core/Ram running in <region>" entry of the requested family.
// Returns USD/vCPU-hour (when wantCore) or USD/GB-hour (when !wantCore).
// Uses the shared per-process SKU cache (gcpListAllSkus) so the
// catalog is fetched once per service and reused across every
// pricing call in the same run.
func (g *gcpFetcher) findCoreRam(family, region, key string, wantCore bool) (float64, error) {
	wantGroup := "CPU"
	if !wantCore {
		wantGroup = "RAM"
	}
	familyUpper := strings.ToUpper(family)
	skus, err := gcpListAllSkus(gcpComputeEngineService, key)
	if err != nil {
		return 0, err
	}
	for _, s := range skus {
		if !inSlice(s.ServiceRegions, region) {
			continue
		}
		if s.Category == nil || s.Category.ResourceFamily != "Compute" {
			continue
		}
		if !strings.Contains(s.Category.ResourceGroup, wantGroup) {
			continue
		}
		if s.Category.UsageType != "OnDemand" {
			continue
		}
		desc := strings.ToUpper(s.Description)
		if !strings.Contains(desc, familyUpper) {
			continue
		}
		if strings.Contains(desc, "PREEMPTIBLE") || strings.Contains(desc, "SPOT") {
			continue
		}
		if strings.Contains(desc, "COMMITMENT") || strings.Contains(desc, "RESERVED") {
			continue
		}
		if strings.Contains(desc, "SOLE TENANT") || strings.Contains(desc, "CUSTOM") {
			continue
		}
		if len(s.PricingInfo) == 0 {
			continue
		}
		price := gcpUsdFromTier(s.PricingInfo[0])
		if price > 0 {
			return price, nil
		}
	}
	return 0, fmt.Errorf("gcp: no %s sku for family %s in %s", wantGroup, family, region)
}

func (g *gcpFetcher) fetchPD(kind, region, key string) (Item, error) {
	wantDesc := "Balanced PD"
	switch kind {
	case "ssd":
		wantDesc = "SSD backed PD Capacity"
	case "standard":
		wantDesc = "Storage PD Capacity"
	case "balanced":
		wantDesc = "Balanced PD Capacity"
	default:
		return Item{}, fmt.Errorf("gcp pd: unknown kind %q", kind)
	}
	skus, err := gcpListAllSkus(gcpComputeEngineService, key)
	if err != nil {
		return Item{}, err
	}
	for _, s := range skus {
		if !inSlice(s.ServiceRegions, region) {
			continue
		}
		if !strings.Contains(s.Description, wantDesc) {
			continue
		}
		if s.Category == nil || s.Category.UsageType != "OnDemand" {
			continue
		}
		if len(s.PricingInfo) == 0 {
			continue
		}
		price := gcpUsdFromTier(s.PricingInfo[0])
		if price > 0 {
			return Item{
				USDPerHour:  0,
				USDPerMonth: price,
				FetchedAt:   time.Now(),
			}, nil
		}
	}
	return Item{}, fmt.Errorf("gcp pd: no %q sku in %s", wantDesc, region)
}

// parseGCPMachineType handles the four GCE machine-type shapes:
//
//   - "<family>-standard-<n>"  vCPU=n, RAM=n×4 GiB
//   - "<family>-highmem-<n>"   vCPU=n, RAM=n×8 GiB
//   - "<family>-highcpu-<n>"   vCPU=n, RAM=n×1 GiB
//   - "<family>-<shared-size>" predefined shared-core types
//     ("e2-micro" / "e2-small" / "e2-medium" /
//     "f1-micro" / "g1-small")
//
// Shared-core types charge a flat per-instance hourly rate rather
// than separable core+RAM lines; we model their RAM using the public
// allocation so downstream cost math (which scales by RAM for
// platform-add-on overhead) still works.
func parseGCPMachineType(mt string) (family string, vcpu int, ramGB float64, err error) {
	parts := strings.Split(strings.ToLower(mt), "-")
	if len(parts) < 2 {
		return "", 0, 0, fmt.Errorf("gcp: unsupported machine type form %q", mt)
	}
	family = parts[0]
	if len(parts) == 2 {
		// Shared-core predefined shapes: <family>-<size>.
		switch family + "-" + parts[1] {
		case "e2-micro":
			return family, 1, 1, nil
		case "e2-small":
			return family, 1, 2, nil
		case "e2-medium":
			return family, 1, 4, nil
		case "f1-micro":
			return family, 1, 0.6, nil
		case "g1-small":
			return family, 1, 1.7, nil
		}
		return "", 0, 0, fmt.Errorf("gcp: unsupported shared-core shape %q", mt)
	}
	predef := parts[1]
	var n int
	if _, e := fmt.Sscanf(parts[2], "%d", &n); e != nil {
		return "", 0, 0, fmt.Errorf("gcp: bad vcpu in %q", mt)
	}
	vcpu = n
	switch predef {
	case "standard":
		ramGB = float64(n) * 4
	case "highmem":
		ramGB = float64(n) * 8
	case "highcpu":
		ramGB = float64(n) * 1
	default:
		return "", 0, 0, fmt.Errorf("gcp: unknown predefined shape %q", predef)
	}
	return family, vcpu, ramGB, nil
}

func inSlice(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// gcpNewCatalogClient creates a GCP CloudCatalogRESTClient authenticated
// with the given API key. The REST transport wraps http.DefaultTransport
// (cloned at creation time) with an API-key parameter injector — this
// means airgap.Apply() must be called before the first GCP pricing call
// in any process. In the normal yage bootstrap flow, airgap is applied
// before any cost-compare runs so this ordering is always satisfied.
func gcpNewCatalogClient(ctx context.Context, key string) (*billing.CloudCatalogClient, error) {
	return billing.NewCloudCatalogRESTClient(ctx, option.WithAPIKey(key))
}

// gcpFetchAllSkus pages through all SKUs for serviceID using the GCP
// billing SDK and returns them as a slice. Used by gcpListAllSkus to
// populate the process-level cache on a cache miss.
func gcpFetchAllSkus(serviceID, key string) ([]*billingpb.Sku, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, err := gcpNewCatalogClient(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("gcp billing client: %w", err)
	}
	defer client.Close()

	req := &billingpb.ListSkusRequest{
		Parent: "services/" + serviceID,
	}
	it := client.ListSkus(ctx, req)

	var out []*billingpb.Sku
	for {
		sku, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gcp sku iter: %w", err)
		}
		out = append(out, sku)
	}
	return out, nil
}
