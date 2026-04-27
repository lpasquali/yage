// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

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
	// DisplayCurrency forces output in a specific ISO-4217 code;
	// empty = auto-detect via DataCenterLocation, then geo-IP, then
	// USD fallback.
	DisplayCurrency string
	// DataCenterLocation is an ISO-3166 alpha-2 country code from
	// --data-center-location; drives currency + region defaults.
	DataCenterLocation string
}

// creds and prefs are the process-globals the orchestrator sets
// once after config.Load. Read-only from the fetchers' perspective.
var (
	creds     Credentials
	prefs     Currency
	airgapped bool
)

// SetAirgapped toggles the airgapped flag used by every pricing
// fetcher. When true, fetchers short-circuit with ErrUnavailable
// (no internet access). Set once at startup from cfg.Airgapped.
// See docs/abstraction-plan.md §17.
func SetAirgapped(b bool) {
	airgapped = b
}

// IsAirgapped reports the current airgapped state.
func IsAirgapped() bool {
	return airgapped
}

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