package config

import (
	"testing"
)

// TestCostCredentialsLoad confirms the §16 contract: each pricing
// vendor's credential is reachable through cfg.Cost.Credentials
// after Load() runs, and the YAGE_X / VENDOR_X dual-spelling
// fallback chain is preserved.
func TestCostCredentialsLoad(t *testing.T) {
	cases := []struct {
		name      string
		envs      map[string]string
		wantField func(c *Config) string
		want      string
	}{
		{
			name:      "gcp prefers YAGE_GCP_API_KEY",
			envs:      map[string]string{"YAGE_GCP_API_KEY": "yk", "GOOGLE_BILLING_API_KEY": "vk"},
			wantField: func(c *Config) string { return c.Cost.Credentials.GCPAPIKey },
			want:      "yk",
		},
		{
			name:      "gcp falls back to GOOGLE_BILLING_API_KEY",
			envs:      map[string]string{"GOOGLE_BILLING_API_KEY": "vk"},
			wantField: func(c *Config) string { return c.Cost.Credentials.GCPAPIKey },
			want:      "vk",
		},
		{
			name:      "hetzner prefers YAGE_HCLOUD_TOKEN",
			envs:      map[string]string{"YAGE_HCLOUD_TOKEN": "yt", "HCLOUD_TOKEN": "vt"},
			wantField: func(c *Config) string { return c.Cost.Credentials.HetznerToken },
			want:      "yt",
		},
		{
			name:      "hetzner falls back to HCLOUD_TOKEN",
			envs:      map[string]string{"HCLOUD_TOKEN": "vt"},
			wantField: func(c *Config) string { return c.Cost.Credentials.HetznerToken },
			want:      "vt",
		},
		{
			name:      "digitalocean prefers YAGE_DO_TOKEN",
			envs:      map[string]string{"YAGE_DO_TOKEN": "ydo", "DIGITALOCEAN_TOKEN": "vdo"},
			wantField: func(c *Config) string { return c.Cost.Credentials.DigitalOceanToken },
			want:      "ydo",
		},
		{
			name:      "ibmcloud prefers YAGE_IBMCLOUD_API_KEY",
			envs:      map[string]string{"YAGE_IBMCLOUD_API_KEY": "yi", "IBMCLOUD_API_KEY": "vi"},
			wantField: func(c *Config) string { return c.Cost.Credentials.IBMCloudAPIKey },
			want:      "yi",
		},
		{
			name:      "currency prefers YAGE_TALLER_CURRENCY",
			envs:      map[string]string{"YAGE_TALLER_CURRENCY": "EUR", "YAGE_CURRENCY": "USD"},
			wantField: func(c *Config) string { return c.Cost.Currency.DisplayCurrency },
			want:      "EUR",
		},
		{
			name:      "EUR_USD override travels through Cost.Currency",
			envs:      map[string]string{"YAGE_EUR_USD": "1.07"},
			wantField: func(c *Config) string { return c.Cost.Currency.EURUSDOverride },
			want:      "1.07",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear all relevant env vars first so each case is hermetic.
			for _, k := range []string{
				"YAGE_GCP_API_KEY", "GOOGLE_BILLING_API_KEY",
				"YAGE_HCLOUD_TOKEN", "HCLOUD_TOKEN",
				"YAGE_DO_TOKEN", "DIGITALOCEAN_TOKEN",
				"YAGE_IBMCLOUD_API_KEY", "IBMCLOUD_API_KEY",
				"YAGE_TALLER_CURRENCY", "YAGE_CURRENCY", "YAGE_EUR_USD",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tc.envs {
				t.Setenv(k, v)
			}
			c := Load()
			if got := tc.wantField(c); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHetznerTokenCrossFill confirms cfg.Cost.Credentials.HetznerToken
// and cfg.Providers.Hetzner.Token cross-fill: same secret, two
// consumers (provider + pricing).
func TestHetznerTokenCrossFill(t *testing.T) {
	t.Run("HCLOUD_TOKEN populates both", func(t *testing.T) {
		t.Setenv("HCLOUD_TOKEN", "shared-token")
		t.Setenv("YAGE_HCLOUD_TOKEN", "")
		c := Load()
		if c.Cost.Credentials.HetznerToken != "shared-token" {
			t.Fatalf("Cost.Credentials.HetznerToken: got %q, want shared-token", c.Cost.Credentials.HetznerToken)
		}
		if c.Providers.Hetzner.Token != "shared-token" {
			t.Fatalf("Providers.Hetzner.Token: got %q, want shared-token (cross-fill)", c.Providers.Hetzner.Token)
		}
	})
}
