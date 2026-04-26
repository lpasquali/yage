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
		return fmt.Sprintf("%s @ $%.3f/GB-mo (live; FX unavailable)", tier, price)
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

// CompareClouds runs the comparison across all registered providers.
// Skips ones that explicitly disable themselves via ErrNotApplicable
// in the cost path; surfaces real errors so the user can see
// missing config or unreachable APIs.
func CompareClouds(cfg *config.Config) []CloudCost {
	out := []CloudCost{}
	for _, name := range provider.Registered() {
		p, err := provider.Get(name)
		if err != nil {
			continue
		}
		est, estErr := p.EstimateMonthlyCostUSD(cfg)
		storagePrice, storageErr := liveBlockStorageUSDPerGBMonth(name, cfg)
		out = append(out, CloudCost{
			ProviderName:         name,
			Estimate:             est,
			Err:                  estErr,
			StorageUSDPerGBMonth: storagePrice,
			StorageErr:           storageErr,
		})
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
	rows := CompareClouds(cfg)
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
		return 0, fmt.Sprintf("compute alone $%.2f exceeds budget $%.2f", computeUSD, budgetUSD)
	}
	return leftover / price, ""
}
