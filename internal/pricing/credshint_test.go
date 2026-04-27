package pricing

import (

	"testing"
)

func TestGCPCredsHint_FromYAGEEnv(t *testing.T) {
	t.Setenv("YAGE_GCP_API_KEY", "abc-123")
	t.Setenv("GOOGLE_BILLING_API_KEY", "")
	creds = Credentials{}
	if !PricingCredsConfigured("gcp") {
		t.Fatalf("PricingCredsConfigured(\"gcp\") = false; YAGE_GCP_API_KEY should be detected")
	}
}

func TestGCPCredsHint_FromGoogleEnv(t *testing.T) {
	t.Setenv("YAGE_GCP_API_KEY", "")
	t.Setenv("GOOGLE_BILLING_API_KEY", "abc-456")
	creds = Credentials{}
	if !PricingCredsConfigured("gcp") {
		t.Fatalf("PricingCredsConfigured(\"gcp\") = false; GOOGLE_BILLING_API_KEY should be detected")
	}
}

func TestGCPCredsHint_FromCfgCreds(t *testing.T) {
	t.Setenv("YAGE_GCP_API_KEY", "")
	t.Setenv("GOOGLE_BILLING_API_KEY", "")
	creds = Credentials{GCPAPIKey: "kind-secret-value"}
	defer func() { creds = Credentials{} }()
	if !PricingCredsConfigured("gcp") {
		t.Fatalf("PricingCredsConfigured(\"gcp\") = false; cfg.Cost.Credentials.GCPAPIKey should be detected")
	}
}
