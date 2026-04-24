package kindsync

import "testing"

func TestParseFlatYAMLOrJSON_YAML(t *testing.T) {
	body := `
# header comment
KIND_VERSION: "v0.31.0"
KUBECTL_VERSION: v1.35.4
ARGOCD_ENABLED: "true"
WORKLOAD_CLUSTER_NAME: 'edge-1'  # inline comment
EMPTY:
not a key
: missing-name
`
	kv := parseFlatYAMLOrJSON(body)
	cases := map[string]string{
		"KIND_VERSION":          "v0.31.0",
		"KUBECTL_VERSION":       "v1.35.4",
		"ARGOCD_ENABLED":        "true",
		"WORKLOAD_CLUSTER_NAME": "edge-1",
		"EMPTY":                 "",
	}
	for k, want := range cases {
		if got := kv[k]; got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
	if _, ok := kv["not a key"]; ok {
		t.Errorf("non-conforming line should not appear")
	}
}

func TestParseFlatYAMLOrJSON_JSON(t *testing.T) {
	body := `{"KIND_VERSION":"v0.31.0","ARGOCD_ENABLED":"false"}`
	kv := parseFlatYAMLOrJSON(body)
	if kv["KIND_VERSION"] != "v0.31.0" {
		t.Errorf("KIND_VERSION: got %q", kv["KIND_VERSION"])
	}
	if kv["ARGOCD_ENABLED"] != "false" {
		t.Errorf("ARGOCD_ENABLED: got %q", kv["ARGOCD_ENABLED"])
	}
}

func TestMigrateLegacyKeys(t *testing.T) {
	kv := map[string]string{
		"BOOT_VOLUME_SIZE":         "50",
		"WORKER_BOOT_VOLUME_SIZE":  "",
		"MEMORY_MIB":               "16384",
		"CONTROL_PLANE_MEMORY_MIB": "8192",
		"TEMPLATE_VMID":            "104",
	}
	migrateLegacyKeys(kv)
	if kv["WORKER_BOOT_VOLUME_SIZE"] != "50" {
		t.Errorf("legacy BOOT_VOLUME_SIZE should copy into WORKER_*: %v", kv)
	}
	if _, ok := kv["BOOT_VOLUME_SIZE"]; ok {
		t.Errorf("legacy BOOT_VOLUME_SIZE should be removed: %v", kv)
	}
	if kv["WORKER_MEMORY_MIB"] != "16384" {
		t.Errorf("MEMORY_MIB should copy into WORKER_MEMORY_MIB: %v", kv)
	}
	if kv["PROXMOX_TEMPLATE_ID"] != "104" {
		t.Errorf("TEMPLATE_VMID should fill PROXMOX_TEMPLATE_ID: %v", kv)
	}
	if _, ok := kv["TEMPLATE_VMID"]; ok {
		t.Errorf("TEMPLATE_VMID should be removed: %v", kv)
	}
}
