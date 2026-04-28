// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

import "testing"

func TestLoadConfigFromSecretData_YAML(t *testing.T) {
	data := map[string][]byte{
		"config.yaml": []byte("KIND_VERSION: v0.31.0\nKUBECTL_VERSION: v1.35.4\n"),
	}
	cfg := LoadConfigFromSecretData(data)
	if cfg.KindVersion != "v0.31.0" {
		t.Errorf("KindVersion = %q, want v0.31.0", cfg.KindVersion)
	}
	if cfg.KubectlVersion != "v1.35.4" {
		t.Errorf("KubectlVersion = %q, want v1.35.4", cfg.KubectlVersion)
	}
}

func TestLoadConfigFromSecretData_JSON(t *testing.T) {
	data := map[string][]byte{
		"config.json": []byte(`{"KIND_VERSION":"v0.31.0"}`),
	}
	cfg := LoadConfigFromSecretData(data)
	if cfg.KindVersion != "v0.31.0" {
		t.Errorf("KindVersion = %q, want v0.31.0", cfg.KindVersion)
	}
}

func TestLoadConfigFromSecretData_Empty(t *testing.T) {
	cfg := LoadConfigFromSecretData(map[string][]byte{})
	if cfg == nil {
		t.Fatal("expected non-nil *config.Config for empty data")
	}
}

func TestLoadConfigFromSecretData_YAMLPreferredOverJSON(t *testing.T) {
	data := map[string][]byte{
		"config.yaml": []byte("KIND_VERSION: from-yaml\n"),
		"config.json": []byte(`{"KIND_VERSION":"from-json"}`),
	}
	cfg := LoadConfigFromSecretData(data)
	if cfg.KindVersion != "from-yaml" {
		t.Errorf("KindVersion = %q, want from-yaml", cfg.KindVersion)
	}
}
