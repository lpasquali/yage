// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"context"
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// TestEnsureIssuingCA_NotApplicable verifies that EnsureIssuingCA returns
// ErrNotApplicable when the root CA material is absent.
func TestEnsureIssuingCA_NotApplicable(t *testing.T) {
	tests := []struct {
		name string
		cert string
		key  string
	}{
		{"both empty", "", ""},
		{"cert empty", "", "some-key"},
		{"key empty", "some-cert", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				IssuingCARootCert: tc.cert,
				IssuingCARootKey:  tc.key,
			}
			err := EnsureIssuingCA(context.Background(), "", cfg)
			if !errors.Is(err, ErrNotApplicable) {
				t.Errorf("want ErrNotApplicable, got %v", err)
			}
		})
	}
}

// TestDestroyIssuingCA_NotApplicable verifies that DestroyIssuingCA returns
// ErrNotApplicable when the root CA material is absent (no module state to
// destroy).
func TestDestroyIssuingCA_NotApplicable(t *testing.T) {
	tests := []struct {
		name string
		cert string
		key  string
	}{
		{"both empty", "", ""},
		{"cert empty", "", "some-key"},
		{"key empty", "some-cert", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				IssuingCARootCert: tc.cert,
				IssuingCARootKey:  tc.key,
			}
			err := DestroyIssuingCA(context.Background(), nil, cfg)
			if !errors.Is(err, ErrNotApplicable) {
				t.Errorf("want ErrNotApplicable, got %v", err)
			}
		})
	}
}

// TestIssuingCAVars_RequiredFields verifies that issuingCAVars includes the
// fields the module requires (cluster_name, root_ca_cert, root_ca_key) and
// that the sensitive root material is forwarded verbatim.
func TestIssuingCAVars_RequiredFields(t *testing.T) {
	cfg := &config.Config{
		WorkloadClusterName: "prod-cluster",
		IssuingCARootCert:   "---ROOT-CERT---",
		IssuingCARootKey:    "---ROOT-KEY---",
	}
	vars := issuingCAVars(cfg)

	checks := map[string]string{
		"cluster_name": "prod-cluster",
		"root_ca_cert": "---ROOT-CERT---",
		"root_ca_key":  "---ROOT-KEY---",
	}
	for k, want := range checks {
		got, ok := vars[k]
		if !ok {
			t.Errorf("issuingCAVars missing required key %q", k)
			continue
		}
		if got != want {
			t.Errorf("vars[%q]: got %q, want %q", k, got, want)
		}
	}
	if len(vars) != len(checks) {
		t.Errorf("issuingCAVars: unexpected extra keys (got %d, want %d): %v", len(vars), len(checks), vars)
	}
}

// TestParseIssuingCAOutputs_BareStrings verifies parsing when tofu emits
// bare string values (no wrapper map). This is the form `tofu output -json`
// uses for non-sensitive scalar outputs.
func TestParseIssuingCAOutputs_BareStrings(t *testing.T) {
	raw := map[string]any{
		"intermediate_cert_pem": "-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----\n",
		"intermediate_key_pem":  "-----BEGIN EC PRIVATE KEY-----\nkey\n-----END EC PRIVATE KEY-----\n",
		"ca_chain_pem":          "-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n",
	}
	out, err := parseIssuingCAOutputs(raw)
	if err != nil {
		t.Fatalf("parseIssuingCAOutputs: %v", err)
	}
	if out.IntermediateCertPEM != raw["intermediate_cert_pem"] {
		t.Errorf("IntermediateCertPEM mismatch")
	}
	if out.IntermediateKeyPEM != raw["intermediate_key_pem"] {
		t.Errorf("IntermediateKeyPEM mismatch")
	}
	if out.CAChainPEM != raw["ca_chain_pem"] {
		t.Errorf("CAChainPEM mismatch")
	}
}

// TestParseIssuingCAOutputs_WrappedValues verifies parsing when tofu emits the
// wrapped {"value": ..., "type": ..., "sensitive": true} form. The intermediate
// key is marked sensitive in the module — `tofu output -json` still emits the
// real value (sensitive only redacts text format), but the wrapper map is the
// shape this parser must handle.
func TestParseIssuingCAOutputs_WrappedValues(t *testing.T) {
	raw := map[string]any{
		"intermediate_cert_pem": map[string]any{
			"value":     "intermediate-cert",
			"type":      "string",
			"sensitive": false,
		},
		"intermediate_key_pem": map[string]any{
			"value":     "intermediate-key",
			"type":      "string",
			"sensitive": true,
		},
		"ca_chain_pem": map[string]any{
			"value":     "chain",
			"type":      "string",
			"sensitive": true,
		},
	}
	out, err := parseIssuingCAOutputs(raw)
	if err != nil {
		t.Fatalf("parseIssuingCAOutputs (wrapped): %v", err)
	}
	if out.IntermediateCertPEM != "intermediate-cert" {
		t.Errorf("IntermediateCertPEM: got %q, want %q", out.IntermediateCertPEM, "intermediate-cert")
	}
	if out.IntermediateKeyPEM != "intermediate-key" {
		t.Errorf("IntermediateKeyPEM: got %q, want %q", out.IntermediateKeyPEM, "intermediate-key")
	}
	if out.CAChainPEM != "chain" {
		t.Errorf("CAChainPEM: got %q, want %q", out.CAChainPEM, "chain")
	}
}

// TestParseIssuingCAOutputs_MissingFields verifies that parseIssuingCAOutputs
// returns an error when required outputs are absent.
func TestParseIssuingCAOutputs_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{
			name: "both required missing",
			raw: map[string]any{
				"ca_chain_pem": "chain",
			},
		},
		{
			name: "key missing",
			raw: map[string]any{
				"intermediate_cert_pem": "cert",
			},
		},
		{
			name: "cert missing",
			raw: map[string]any{
				"intermediate_key_pem": "key",
			},
		},
		{
			name: "empty map",
			raw:  map[string]any{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseIssuingCAOutputs(tc.raw)
			if err == nil {
				t.Error("expected error when required outputs are missing, got nil")
			}
		})
	}
}
