// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cost is the multi-cloud comparator: given one logical
// cluster shape, run every registered provider's
// EstimateMonthlyCostUSD and surface the results side-by-side.
//
// Pairs with a per-cloud "block storage USD/GB-month" lookup so the
// dry-run can answer "given the budget X, how much persistent
// storage retention can I afford after compute?" — useful sizing
// signal especially for log/observability and database buckets.
//
// All $/GB-month numbers are fetched LIVE from each vendor's
// FinOps / billing API at the moment of comparison. There are no
// hardcoded money numbers — when an API is unreachable, the cell
// shows "(unavailable)" instead of substituting a stale figure.
package cost

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// liveBlockStorageUSDPerGBMonth fetches the cheap-tier block
// storage $/GB-month for the named provider, against the same
// region the planned cluster will use. Returns 0 + error when the
// provider has no priced storage (Proxmox/CAPD/vSphere — self-
// hosted, sunk cost) or when the live API is unreachable.
//
// Provider → SKU mapping (cheap tier only):
//   aws     → "ebs:gp3"  (live Bulk Pricing JSON, region from cfg.Providers.AWS.Region)
//   azure   → "Standard SSD Managed Disks" (live Retail Prices API,
//                                            region from cfg.Providers.Azure.Location)
//   gcp     → "pd:balanced" (live Cloud Billing Catalog,
//                            region from cfg.Providers.GCP.Region; needs API key)
//   hetzner → live volume rate from /v1/pricing
//   anything else → 0, ErrNotApplicable
func liveBlockStorageUSDPerGBMonth(provName string, cfg *config.Config) (float64, error) {
	switch provName {
	case "aws":
		region := cfg.Providers.AWS.Region
		if region == "" {
			region = "us-east-1"
		}
		it, err := pricing.Fetch("aws", "ebs:gp3", region)
		if err != nil {
			return 0, err
		}
		return it.USDPerMonth, nil
	case "azure":
		region := cfg.Providers.Azure.Location
		if region == "" {
			region = "eastus"
		}
		return pricing.AzureManagedDiskUSDPerGBMonth(region, "Standard SSD Managed Disks")
	case "gcp":
		region := cfg.Providers.GCP.Region
		if region == "" {
			region = "us-central1"
		}
		it, err := pricing.Fetch("gcp", "pd:balanced", region)
		if err != nil {
			return 0, err
		}
		return it.USDPerMonth, nil
	case "hetzner":
		return pricing.HetznerVolumeUSDPerGBMonth()
	}
	return 0, provider.ErrNotApplicable
}

// LiveBlockStorageLabel returns a human-readable label for the
// provider's cheap-tier block storage. Used by plan.go in the
// retention section. Live $/GB-month is queried per call and
// converted into the active taller for display.
func LiveBlockStorageLabel(provName string, cfg *config.Config) string {
	tier := blockStorageTierName(provName)
	if tier == "" {
		return ""
	}
	price, err := liveBlockStorageUSDPerGBMonth(provName, cfg)
	if err != nil || price <= 0 {
		return tier + " (live price unavailable)"
	}
	conv, _, ferr := pricing.ToTaller(price, "USD")
	if ferr != nil {
		return fmt.Sprintf("%s @ %s/GB-mo (live; FX unavailable)",
			tier, pricing.FormatTaller(price, "USD"))
	}
	return fmt.Sprintf("%s @ %s%.3f/GB-mo (live)", tier, pricing.TallerSymbol(), conv)
}

func blockStorageTierName(provName string) string {
	switch provName {
	case "aws":
		return "EBS gp3"
	case "azure":
		return "Standard SSD"
	case "gcp":
		return "pd-balanced"
	case "hetzner":
		return "HCloud Volume"
	}
	return ""
}

// CloudCost is one row in the multi-cloud comparison table.
type CloudCost struct {
	ProviderName         string
	Estimate             provider.CostEstimate
	Err                  error
	StorageUSDPerGBMonth float64 // 0 when not fetchable
	StorageErr           error   // err describing why storage price is missing
}

// CompareClouds runs the comparison across all registered providers
// in parallel — every vendor hits its own catalog/billing endpoint,
// there is no shared rate limit between vendors, and the per-vendor
// call count is identical to the sequential path. Progress lines
// surface as each vendor finishes (out-of-order; the final result
// list is re-sorted deterministically).
//
// Skips providers that explicitly disable themselves via
// ErrNotApplicable in the cost path; surfaces real errors so the
// user can see missing config or unreachable APIs.
func CompareClouds(cfg *config.Config, progress io.Writer) []CloudCost {
	return CompareWithFilter(cfg, ScopeAll, progress)
}

// Scope narrows the provider set CompareWithFilter iterates over.
// "Cloud-only" drops on-prem providers (Proxmox/vSphere/OpenStack/
// CAPD) so the cloud-fork doesn't compare against TCO rows, which
// require operator-supplied hardware inputs. "On-prem-only" is the
// mirror image, used by the on-prem fork's optional TCO step.
type Scope int

const (
	ScopeAll       Scope = iota // every registered provider (subject to airgap filter)
	ScopeCloudOnly              // hyperscale + managed-cloud providers only
	ScopeOnPremOnly             // proxmox / vsphere / openstack / docker (CAPD)
)

// StreamWithFilter fans out provider cost fetches in parallel and sends each
// CloudCost to ch as it completes, then closes ch. Callers should not read
// ch on the same goroutine as the call — the function returns immediately
// after launching the worker goroutines; ch is closed by an internal watcher
// once all workers finish.
//
// Progress is written before any goroutines launch (count header) and after
// each result arrives (per-provider tick line).
func StreamWithFilter(cfg *config.Config, scope Scope, progress io.Writer, ch chan<- CloudCost) {
	names := provider.AirgapFilter(provider.Registered(), cfg.Airgapped)
	names = filterEphemeralTestProviders(names)
	switch scope {
	case ScopeCloudOnly:
		names = filterCloudOnly(names)
	case ScopeOnPremOnly:
		names = filterOnPremOnly(names)
	}
	if cfg.InfraProvider != "" && !cfg.InfraProviderDefaulted {
		names = filterByInclusion(names, map[string]struct{}{cfg.InfraProvider: {}})
	}
	skipped := parseProviderList(cfg.SkipProviders)
	if len(skipped) > 0 {
		names = filterByExclusion(names, skipped)
	}
	if progress != nil {
		switch {
		case cfg.InfraProvider != "" && !cfg.InfraProviderDefaulted && len(skipped) > 0:
			fmt.Fprintf(progress, "  live cost compare: %d provider(s) (infra-provider: %s; skipping: %s)\n",
				len(names), cfg.InfraProvider,
				strings.Join(sortedKeys(skipped), ", "))
		case cfg.InfraProvider != "" && !cfg.InfraProviderDefaulted:
			fmt.Fprintf(progress, "  live cost compare: %d provider(s) (infra-provider: %s)\n",
				len(names), cfg.InfraProvider)
		case len(skipped) > 0:
			fmt.Fprintf(progress, "  live cost compare: %d provider(s) (skipping: %s)\n",
				len(names), strings.Join(sortedKeys(skipped), ", "))
		default:
			fmt.Fprintf(progress, "  live cost compare: %d provider(s)\n", len(names))
		}
	}
	var wg sync.WaitGroup
	for _, name := range names {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := provider.Get(name)
			if err != nil {
				return // skip unknown/unregistered — no send
			}
			est, estErr := p.EstimateMonthlyCostUSD(cfg)
			storagePrice, storageErr := liveBlockStorageUSDPerGBMonth(name, cfg)
			ch <- CloudCost{
				ProviderName:         name,
				Estimate:             est,
				Err:                  estErr,
				StorageUSDPerGBMonth: storagePrice,
				StorageErr:           storageErr,
			}
			if progress != nil {
				fmt.Fprintf(progress, "    ✓ %s\n", name)
			}
		}()
	}
	go func() { wg.Wait(); close(ch) }()
}

// CompareWithFilter is CompareClouds with an explicit scope filter.
// Cloud-fork callers pass ScopeCloudOnly so on-prem rows don't
// pollute the cost compare (they'd all be (estimator error) without
// --hardware-cost-usd anyway). CAPD (CAPI's Docker reference
// provider) is dropped at every scope: it's an ephemeral test path,
// not a deployment target — pricing it would just confuse the
// table.
func CompareWithFilter(cfg *config.Config, scope Scope, progress io.Writer) []CloudCost {
	ch := make(chan CloudCost, 32)
	StreamWithFilter(cfg, scope, progress, ch)
	var out []CloudCost
	for r := range ch {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := out[i], out[j]
		ciPriced := ci.Err == nil && ci.Estimate.TotalUSDMonthly > 0
		cjPriced := cj.Err == nil && cj.Estimate.TotalUSDMonthly > 0
		if ciPriced && !cjPriced {
			return true
		}
		if !ciPriced && cjPriced {
			return false
		}
		if ciPriced && cjPriced {
			return ci.Estimate.TotalUSDMonthly < cj.Estimate.TotalUSDMonthly
		}
		return ci.ProviderName < cj.ProviderName
	})
	return out
}

// PrintComparison writes a human-readable comparison table to w.
// Each row: provider | monthly | live $/GB-mo | retention budget.
// Footer notes which providers were unpriced or had API failures.
func PrintComparison(w io.Writer, cfg *config.Config) {
	rows := CompareClouds(cfg, nil)
	hr := func() {
		fmt.Fprintln(w, "─────────────────────────────────────────────────────────────────────────────")
	}
	hr()
	fmt.Fprintln(w, "🌍 MULTI-CLOUD COST COMPARISON — same cluster shape, every registered provider")
	fmt.Fprintln(w, "    (every monetary value is live from the vendor's billing API,")
	fmt.Fprintln(w, "     converted into the active taller currency at live FX)")
	fmt.Fprintln(w, "    "+pricing.TallerNote())
	hr()
	sym := pricing.TallerSymbol()
	fmt.Fprintf(w, "  %-10s  %14s  %16s  %s\n", "provider",
		"monthly "+sym, sym+"/GB-mo (live)", "max retention if budget ÷ storage")
	for _, r := range rows {
		if r.Err != nil {
			fmt.Fprintf(w, "  %-10s  %14s  %16s  %s\n", r.ProviderName, "—", "—", "(estimator: "+r.Err.Error()+")")
			continue
		}
		monthlyStr := pricing.FormatTaller(r.Estimate.TotalUSDMonthly, "USD")
		gbStr := "—"
		if r.StorageUSDPerGBMonth > 0 {
			converted, _, err := pricing.ToTaller(r.StorageUSDPerGBMonth, "USD")
			if err != nil {
				gbStr = fmt.Sprintf("$%.3f (FX!)", r.StorageUSDPerGBMonth)
			} else {
				gbStr = fmt.Sprintf("%s%.3f", sym, converted)
			}
		} else if r.StorageErr != nil {
			gbStr = "(unavail)"
		}
		fmt.Fprintf(w, "  %-10s  %14s  %16s  %s\n",
			r.ProviderName, monthlyStr, gbStr,
			retentionDescription(r.Estimate.TotalUSDMonthly, r.StorageUSDPerGBMonth))
	}
	hr()
	fmt.Fprintln(w, "Retention column: same total budget reallocated to block storage at the cloud's")
	fmt.Fprintln(w, "live list price. Useful for sizing log/observability + DB buckets — pick the cloud")
	fmt.Fprintln(w, "where your storage envelope is widest if persistence is the dominant cost driver.")
	hr()

	// First-run onboarding hints — for every vendor where pricing
	// failed because creds aren't configured, print the IAM/token
	// setup snippet ONCE per cache.
	for _, r := range rows {
		if r.Err == nil {
			continue
		}
		pricing.MaybePrintOnboarding(w, r.ProviderName)
	}
}

// ephemeralTestProviders are registry entries that exist for
// orchestration testing rather than as real deployment targets.
// They have no meaningful cost story and showing them in the
// compare table only confuses operators ("why is Docker on this
// cloud-cost list?"). The CAPD provider registers itself under the
// name "docker" — that's the key used here.
var ephemeralTestProviders = map[string]struct{}{
	"docker": {},
}

// filterEphemeralTestProviders drops capd-style entries that have
// no real cost surface. Applied at every scope.
func filterEphemeralTestProviders(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, skip := ephemeralTestProviders[n]; skip {
			continue
		}
		out = append(out, n)
	}
	return out
}

// parseProviderList splits a comma-separated list of registry
// names into a lowercased set. Empty values and whitespace are
// dropped so "aws, ,gcp" yields {"aws", "gcp"}. Used by
// --skip-providers.
func parseProviderList(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out[p] = struct{}{}
		}
	}
	return out
}

// filterByExclusion drops names that are in the skip set. Order is
// preserved.
func filterByExclusion(names []string, skip map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, drop := skip[n]; drop {
			continue
		}
		out = append(out, n)
	}
	return out
}

// filterByInclusion keeps only names that are in the allow set.
// Order is preserved.
func filterByInclusion(names []string, allow map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := allow[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// sortedKeys returns the keys of a string-set sorted alphabetically.
// Used to render a stable "skipping: a, b, c" line.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// filterCloudOnly drops on-prem providers from names. The
// classification reuses provider.AirgapCompatible — every airgap-
// compatible provider in the registry is on-prem (their cost path
// is TCO-driven, not vendor-API-driven), and every other registered
// provider is a hyperscale / managed cloud.
func filterCloudOnly(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !provider.AirgapCompatible(n) {
			out = append(out, n)
		}
	}
	return out
}

// filterOnPremOnly is the mirror of filterCloudOnly.
func filterOnPremOnly(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if provider.AirgapCompatible(n) {
			out = append(out, n)
		}
	}
	return out
}

func retentionDescription(budgetUSD, pricePerGBMonth float64) string {
	if pricePerGBMonth <= 0 || budgetUSD <= 0 {
		return "(unpriced)"
	}
	gb := budgetUSD / pricePerGBMonth
	if gb >= 1024 {
		return fmt.Sprintf("%.1f TiB / month if all storage", gb/1024)
	}
	return fmt.Sprintf("%.0f GB / month if all storage", gb)
}

// RetentionAtBudget returns how many GB of cheap-tier block
// storage a given USD budget buys on a specific cloud, after
// subtracting the compute estimate. Live $/GB-month from the
// vendor API.
//
// When the cloud's compute estimate exceeds the budget, returns
// 0 + a "compute alone is over budget" note so the dry-run can
// warn the user before they think they have storage room.
func RetentionAtBudget(provName string, cfg *config.Config, budgetUSD, computeUSD float64) (gb float64, note string) {
	price, err := liveBlockStorageUSDPerGBMonth(provName, cfg)
	if err != nil || price <= 0 {
		if err != nil {
			return 0, "live storage price unavailable: " + err.Error()
		}
		return 0, "self-hosted / unpriced storage"
	}
	leftover := budgetUSD - computeUSD
	if leftover <= 0 {
		return 0, fmt.Sprintf("compute alone %s exceeds budget %s",
			pricing.FormatTaller(computeUSD, "USD"),
			pricing.FormatTaller(budgetUSD, "USD"))
	}
	return leftover / price, ""
}