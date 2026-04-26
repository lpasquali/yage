package pricing

// Credentials carries the per-vendor pricing API credentials that
// the orchestrator (`cmd/yage/main.go`) hands to the pricing
// package once at startup. Pricing fetchers read from the
// package-level `creds` global rather than calling os.Getenv
// directly — this keeps the credential-source decision (env var
// today, kind Secret after Phase D) in one place.
//
// See docs/abstraction-plan.md §16.
type Credentials struct {
	GCPAPIKey         string // Google Cloud Billing Catalog
	HetznerToken      string // Hetzner Cloud project token
	DigitalOceanToken string // DigitalOcean API token
	IBMCloudAPIKey    string // IBM Cloud API key
}

// Currency carries cost-display locale / FX preferences. Not
// secrets, but they share the same orchestrator → pricing-package
// hand-off path: `cmd/yage/main.go` populates them from
// cfg.Cost.Currency at startup.
type Currency struct {
	DisplayCurrency string // ISO currency code (e.g. "EUR"); empty = auto-detect
	EURUSDOverride  string // manual EUR/USD pin when open.er-api.com is unreachable
}

// creds and prefs are the process-globals the orchestrator sets
// once after config.Load. Read-only from the fetchers' perspective.
var (
	creds Credentials
	prefs Currency
)

// SetCredentials installs the credential set used by pricing
// fetchers. Call once at program start, after config.Load.
func SetCredentials(c Credentials) {
	creds = c
}

// SetCurrency installs the currency / FX preference set used by
// pricing fetchers and the taller (display-currency) layer. Call
// once at program start, after config.Load.
func SetCurrency(c Currency) {
	prefs = c
}

// GetCredentials returns a copy of the current credential set.
// Useful for tests and for the xapiri TUI to display "what's
// already wired" without re-reading env.
func GetCredentials() Credentials {
	return creds
}

// GetCurrency returns a copy of the current currency preferences.
func GetCurrency() Currency {
	return prefs
}
