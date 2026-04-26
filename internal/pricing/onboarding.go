package pricing

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Onboarding hints — when bootstrap-capi tries to fetch live pricing
// for a cloud and detects no credentials configured, it prints the
// exact CLI snippet the user would run to set up the minimum-
// permission identity for pricing-only access. Cost APIs are free
// to call across all four vendors (AWS pricing:GetProducts, Azure
// Retail Prices anonymous, GCP Cloud Billing Catalog with API key,
// Hetzner project token); the hint is purely a setup nudge so the
// dry-run can switch from the unauthenticated fallback path (or
// nothing-priced) to the authenticated, faster path.
//
// Display contract: shown ONCE per vendor per cache directory.
// A sentinel file at <cacheDir>/.onboarded-<vendor> records that
// the user has seen the hint. Force re-display via env
// BOOTSTRAP_CAPI_FORCE_PRICING_ONBOARDING=1, suppress entirely via
// BOOTSTRAP_CAPI_NO_PRICING_ONBOARDING=1.

// PricingCredsConfigured returns true when the program has what it
// needs to call the *authenticated* pricing path for vendor.
//
//   aws          → ~/.aws/credentials, AWS_ACCESS_KEY_ID, AWS_PROFILE
//                  (Bulk JSON works without creds; this checks the
//                   SDK path that GetProducts will switch to.)
//   azure        → always true (Retail Prices API is anonymous)
//   gcp          → BOOTSTRAP_CAPI_GCP_API_KEY / GOOGLE_BILLING_API_KEY
//   hetzner      → HCLOUD_TOKEN / BOOTSTRAP_CAPI_HCLOUD_TOKEN
//   digitalocean → DIGITALOCEAN_TOKEN / BOOTSTRAP_CAPI_DO_TOKEN
//   linode       → always true (catalog is anonymous)
//   oci          → always true (Cost Estimator JSON is anonymous)
//   ibmcloud     → IBMCLOUD_API_KEY / BOOTSTRAP_CAPI_IBMCLOUD_API_KEY
func PricingCredsConfigured(vendor string) bool {
	switch vendor {
	case "aws":
		if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
			return true
		}
		if os.Getenv("AWS_PROFILE") != "" {
			return true
		}
		home, err := os.UserHomeDir()
		if err == nil {
			for _, p := range []string{".aws/credentials", ".aws/config"} {
				if _, e := os.Stat(filepath.Join(home, p)); e == nil {
					return true
				}
			}
		}
		return false
	case "azure", "linode", "oci":
		return true
	case "gcp":
		return gcpAPIKey() != ""
	case "hetzner":
		return hetznerToken() != ""
	case "digitalocean":
		return doToken() != ""
	case "ibmcloud":
		return ibmAPIKey() != ""
	}
	return true
}

// OnboardingHint returns the multi-line CLI / web snippet to set up
// the minimum-permission identity for vendor's pricing API. Empty
// string when no setup is needed (Azure).
func OnboardingHint(vendor string) string {
	switch vendor {
	case "aws":
		return awsOnboardingHint
	case "gcp":
		return gcpOnboardingHint
	case "hetzner":
		return hetznerOnboardingHint
	case "digitalocean":
		return doOnboardingHint
	case "ibmcloud":
		return ibmOnboardingHint
	case "azure", "linode", "oci":
		return ""
	}
	return ""
}

const awsOnboardingHint = `AWS pricing API needs an IAM identity (the data is public, but
boto3 / aws-sdk-go reject calls without creds). The user below has
read-only access to pricing:* and nothing else — about as low-risk
a credential as you can issue.

  # 1. Create a dedicated IAM user
  aws iam create-user --user-name bootstrap-capi-pricing

  # 2. Attach a least-privilege inline policy (read-only pricing)
  aws iam put-user-policy \
    --user-name bootstrap-capi-pricing \
    --policy-name PricingReadOnly \
    --policy-document '{
      "Version": "2012-10-17",
      "Statement": [{
        "Effect": "Allow",
        "Action": [
          "pricing:GetProducts",
          "pricing:DescribeServices",
          "pricing:GetAttributeValues"
        ],
        "Resource": "*"
      }]
    }'

  # 3. Generate an access key
  aws iam create-access-key --user-name bootstrap-capi-pricing

  # 4. Save under an isolated profile (don't pollute your default)
  aws configure --profile bootstrap-capi-pricing
  #   (paste AccessKeyId / SecretAccessKey from step 3,
  #    region us-east-1, output json)

  # 5. Tell bootstrap-capi to use that profile
  export AWS_PROFILE=bootstrap-capi-pricing

Pricing API calls are FREE — they don't appear on your bill. (Don't
confuse with Cost Explorer at $0.01/request — different API, not
used here.)`

const gcpOnboardingHint = `GCP pricing needs a Cloud Billing API key. A separate, empty
project keeps the key isolated from your real workloads.

  # 1. Create or pick a project (a fresh one is cleanest)
  gcloud projects create bootstrap-capi-pricing \
    --name="bootstrap-capi pricing"
  gcloud config set project bootstrap-capi-pricing

  # 2. Enable the Cloud Billing API
  gcloud services enable cloudbilling.googleapis.com

  # 3. Create an API key restricted to that API
  gcloud alpha services api-keys create \
    --display-name="bootstrap-capi pricing" \
    --api-target=service=cloudbilling.googleapis.com

  # 4. List keys to copy the keyString
  gcloud alpha services api-keys list \
    --filter='displayName:"bootstrap-capi pricing"' \
    --format='value(name)' \
    | xargs -I {} gcloud alpha services api-keys get-key-string {}

  # 5. Export it
  export BOOTSTRAP_CAPI_GCP_API_KEY="<keyString>"

Catalog API calls are covered by the default free quota.`

const doOnboardingHint = `DigitalOcean catalog needs a personal API token. The "Read"
scope is enough — tokens are project-scoped to the team that
created them.

  # 1. Open the DigitalOcean control panel:
  #    https://cloud.digitalocean.com/account/api/tokens

  # 2. Click "Generate New Token"
  #    Name: bootstrap-capi pricing
  #    Expiration: pick a duration (90d is reasonable)
  #    Scopes: "Read" (full read is enough; no write scopes needed)

  # 3. Copy the token (shown ONCE), then export it
  export DIGITALOCEAN_TOKEN="<token>"

  # OR with doctl (token is interactive):
  doctl auth init --context bootstrap-capi-pricing

The /v2/sizes catalog is metered against the standard rate limit
(5000 req/h) but doesn't appear on your bill — DigitalOcean doesn't
charge for catalog reads.`

const ibmOnboardingHint = `IBM Cloud Global Catalog needs an IAM API key. Service IDs
(machine-account credentials) are the right shape for a CI/scripted
caller — they don't need a human user behind them.

  # 1. Create a Service ID
  ibmcloud iam service-id-create bootstrap-capi-pricing \
    --description "bootstrap-capi pricing reader"

  # 2. Attach a read-only policy on the Global Catalog
  ibmcloud iam service-policy-create bootstrap-capi-pricing \
    --roles Viewer \
    --service-name globalcatalog

  # 3. Generate an API key for the Service ID
  ibmcloud iam service-api-key-create bootstrap-capi-pricing-key \
    bootstrap-capi-pricing

  # 4. Copy the API Key value from the output, then export
  export IBMCLOUD_API_KEY="<api-key>"

The Global Catalog API is free; pricing is exchanged for a Bearer
token via the IAM /identity/token endpoint, also free.`

const hetznerOnboardingHint = `Hetzner Cloud API needs a project token. Tokens are project-scoped,
so create a dedicated empty project to keep the token blast radius
to a minimum.

  # CLI is web-only for token creation; from a browser:

  # 1. Open the Hetzner Cloud Console
  #    https://console.hetzner.cloud/

  # 2. Click "+ NEW PROJECT", name it (e.g. bootstrap-capi-pricing)

  # 3. In that project: Security → API Tokens → "Generate API Token"
  #    Description: bootstrap-capi pricing
  #    Permissions: READ  (read-only — catalog queries don't need write)

  # 4. Copy the token (shown ONCE), then export it

  export HCLOUD_TOKEN="<token>"

Catalog calls don't count against billing (Hetzner doesn't meter
catalog reads at all).`

// MaybePrintOnboarding writes the hint for vendor to w when:
//   - the user hasn't seen it yet (no sentinel in cacheDir), and
//   - credentials are not configured, and
//   - the vendor has a hint to give (Azure: none).
//
// Force-replay via BOOTSTRAP_CAPI_FORCE_PRICING_ONBOARDING=1.
// Suppress entirely via BOOTSTRAP_CAPI_NO_PRICING_ONBOARDING=1.
//
// Returns true when the hint was actually printed; callers can use
// that to add a section header / separator without printing one
// when the hint was skipped.
func MaybePrintOnboarding(w io.Writer, vendor string) bool {
	if os.Getenv("BOOTSTRAP_CAPI_NO_PRICING_ONBOARDING") == "1" {
		return false
	}
	if PricingCredsConfigured(vendor) {
		return false
	}
	hint := OnboardingHint(vendor)
	if hint == "" {
		return false
	}
	force := os.Getenv("BOOTSTRAP_CAPI_FORCE_PRICING_ONBOARDING") == "1"
	if !force && onboardingShown(vendor) {
		return false
	}
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "💡 First-run setup hint — %s\n", strings.ToUpper(vendor))
	fmt.Fprintln(w, strings.Repeat("─", 77))
	fmt.Fprintln(w, hint)
	fmt.Fprintln(w, strings.Repeat("─", 77))
	fmt.Fprintln(w, "Suppress this hint: export BOOTSTRAP_CAPI_NO_PRICING_ONBOARDING=1")
	fmt.Fprintln(w, "Force re-display:   export BOOTSTRAP_CAPI_FORCE_PRICING_ONBOARDING=1")
	fmt.Fprintln(w, "")
	if !force {
		markOnboardingShown(vendor)
	}
	return true
}

// PrintOnboardingForce always prints the hint, ignoring the
// sentinel and PricingCredsConfigured. Used by the explicit
// `--print-pricing-setup VENDOR` CLI command.
func PrintOnboardingForce(w io.Writer, vendor string) {
	hint := OnboardingHint(vendor)
	if hint == "" {
		fmt.Fprintf(w, "%s pricing needs no setup (anonymous public API).\n", strings.ToUpper(vendor))
		return
	}
	fmt.Fprintf(w, "💡 %s pricing — IAM / token setup\n", strings.ToUpper(vendor))
	fmt.Fprintln(w, strings.Repeat("─", 77))
	fmt.Fprintln(w, hint)
	fmt.Fprintln(w, strings.Repeat("─", 77))
}

func onboardingShown(vendor string) bool {
	_, err := os.Stat(filepath.Join(cacheDir(), ".onboarded-"+vendor))
	return err == nil
}

func markOnboardingShown(vendor string) {
	_ = os.MkdirAll(cacheDir(), 0o755)
	path := filepath.Join(cacheDir(), ".onboarded-"+vendor)
	_ = os.WriteFile(path, []byte(""), 0o644)
}
