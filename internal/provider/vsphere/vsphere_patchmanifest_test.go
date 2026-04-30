// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package vsphere

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// minimalManifest is a trimmed-down multi-document YAML that mirrors the
// structure of k3sTemplate: one Cluster, one VSphereCluster, two
// VSphereMachineTemplate (control-plane + worker), one KThreesControlPlane,
// one MachineDeployment, one KThreesConfigTemplate.
//
// The VSphereMachineTemplate docs use the same field ordering as the real
// template (diskGiB / memoryMiB / numCPUs / numCoresPerSocket).
const minimalManifest = `apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: test-cluster
  namespace: default
spec:
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: VSphereCluster
    name: test-cluster
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereCluster
metadata:
  name: test-cluster
  namespace: default
spec:
  server: vcenter.example.com
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachineTemplate
metadata:
  name: test-cluster-control-plane
  namespace: default
spec:
  template:
    spec:
      diskGiB: 25
      memoryMiB: 4096
      numCPUs: 2
      numCoresPerSocket: 1
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta2
kind: KThreesControlPlane
metadata:
  name: test-cluster-control-plane
  namespace: default
spec:
  machineTemplate:
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: VSphereMachineTemplate
      name: test-cluster-control-plane
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineDeployment
metadata:
  name: test-cluster-md-0
  namespace: default
spec:
  template:
    spec:
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: VSphereMachineTemplate
        name: test-cluster-md-0
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachineTemplate
metadata:
  name: test-cluster-md-0
  namespace: default
spec:
  template:
    spec:
      diskGiB: 25
      memoryMiB: 4096
      numCPUs: 2
      numCoresPerSocket: 1
---
apiVersion: orchestrator.cluster.x-k8s.io/v1beta2
kind: KThreesConfigTemplate
metadata:
  name: test-cluster-md-0
  namespace: default
spec:
  template:
    spec:
      agentConfig: {}
`

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "manifest-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

func readManifest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return string(raw)
}

// TestPatchManifest_NoopWhenFieldsEmpty verifies that PatchManifest is a
// no-op when no sizing fields are set on cfg.
func TestPatchManifest_NoopWhenFieldsEmpty(t *testing.T) {
	path := writeManifest(t, minimalManifest)
	cfg := &config.Config{}
	p := &Provider{}
	if err := p.PatchManifest(cfg, path, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := readManifest(t, path)
	if got != minimalManifest {
		t.Errorf("manifest was modified when all sizing fields are empty")
	}
}

// TestPatchManifest_NoopForMgmt verifies that mgmt=true is always a no-op.
func TestPatchManifest_NoopForMgmt(t *testing.T) {
	path := writeManifest(t, minimalManifest)
	cfg := &config.Config{}
	cfg.Providers.Vsphere.ControlPlaneNumCPUs = "8"
	cfg.Providers.Vsphere.WorkerNumCPUs = "4"
	p := &Provider{}
	if err := p.PatchManifest(cfg, path, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := readManifest(t, path)
	if got != minimalManifest {
		t.Errorf("manifest was modified for mgmt=true")
	}
}

// TestPatchManifest_ControlPlaneAndWorkerSizing verifies that the correct
// sizing fields are written into each VSphereMachineTemplate document and
// that unrelated documents are untouched.
func TestPatchManifest_ControlPlaneAndWorkerSizing(t *testing.T) {
	path := writeManifest(t, minimalManifest)
	cfg := &config.Config{}
	cfg.Providers.Vsphere.ControlPlaneNumCPUs = "4"
	cfg.Providers.Vsphere.ControlPlaneNumCoresPerSocket = "2"
	cfg.Providers.Vsphere.ControlPlaneMemoryMiB = "8192"
	cfg.Providers.Vsphere.ControlPlaneDiskGiB = "50"
	cfg.Providers.Vsphere.WorkerNumCPUs = "2"
	cfg.Providers.Vsphere.WorkerNumCoresPerSocket = "1"
	cfg.Providers.Vsphere.WorkerMemoryMiB = "4096"
	cfg.Providers.Vsphere.WorkerDiskGiB = "30"

	p := &Provider{}
	if err := p.PatchManifest(cfg, path, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := readManifest(t, path)
	docs := strings.Split(got, "\n---\n")

	// Use line-anchored matching (same rule as PatchManifest itself):
	// the KThreesControlPlane doc contains `kind: VSphereMachineTemplate`
	// inside its infrastructureRef — a plain Contains would match it.
	vmtKindLine := regexp.MustCompile(`(?m)^kind:\s*VSphereMachineTemplate\s*$`)
	var cpDoc, wkDoc string
	for _, d := range docs {
		if !vmtKindLine.MatchString(d) {
			continue
		}
		if strings.Contains(d, "name: test-cluster-control-plane") {
			cpDoc = d
		} else if strings.Contains(d, "name: test-cluster-md-0") {
			wkDoc = d
		}
	}

	// Control-plane assertions.
	for _, want := range []string{
		"numCPUs: 4",
		"numCoresPerSocket: 2",
		"memoryMiB: 8192",
		"diskGiB: 50",
	} {
		if !strings.Contains(cpDoc, want) {
			t.Errorf("control-plane doc missing %q\ndoc:\n%s", want, cpDoc)
		}
	}
	// Worker assertions.
	for _, want := range []string{
		"numCPUs: 2",
		"numCoresPerSocket: 1",
		"memoryMiB: 4096",
		"diskGiB: 30",
	} {
		if !strings.Contains(wkDoc, want) {
			t.Errorf("worker doc missing %q\ndoc:\n%s", want, wkDoc)
		}
	}

	// Documents that are not VSphereMachineTemplate must be identical to
	// the originals (verifies the line-anchored kind check).
	origDocs := strings.Split(minimalManifest, "\n---\n")
	for i, orig := range origDocs {
		if vmtKindLine.MatchString(orig) {
			continue
		}
		if docs[i] != orig {
			t.Errorf("non-VMT document %d was modified:\noriginal:\n%s\ngot:\n%s", i, orig, docs[i])
		}
	}
}

// TestPatchManifest_PartialFields verifies that only explicitly set fields
// are rewritten; unset fields keep the original value.
func TestPatchManifest_PartialFields(t *testing.T) {
	path := writeManifest(t, minimalManifest)
	cfg := &config.Config{}
	// Only set memory for control-plane; leave CPU/disk/worker untouched.
	cfg.Providers.Vsphere.ControlPlaneMemoryMiB = "16384"

	p := &Provider{}
	if err := p.PatchManifest(cfg, path, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := readManifest(t, path)
	docs := strings.Split(got, "\n---\n")

	vmtKindLine := regexp.MustCompile(`(?m)^kind:\s*VSphereMachineTemplate\s*$`)
	var cpDoc string
	for _, d := range docs {
		if vmtKindLine.MatchString(d) && strings.Contains(d, "name: test-cluster-control-plane") {
			cpDoc = d
		}
	}
	if !strings.Contains(cpDoc, "memoryMiB: 16384") {
		t.Errorf("control-plane doc missing patched memoryMiB: 16384\ndoc:\n%s", cpDoc)
	}
	// Other fields must remain at their original values.
	if !strings.Contains(cpDoc, "numCPUs: 2") {
		t.Errorf("control-plane numCPUs should be unchanged (2)\ndoc:\n%s", cpDoc)
	}
	if !strings.Contains(cpDoc, "diskGiB: 25") {
		t.Errorf("control-plane diskGiB should be unchanged (25)\ndoc:\n%s", cpDoc)
	}

	// Worker doc must be identical to original.
	origDocs := strings.Split(minimalManifest, "\n---\n")
	for i, orig := range origDocs {
		if vmtKindLine.MatchString(orig) && strings.Contains(orig, "name: test-cluster-md-0") {
			if docs[i] != orig {
				t.Errorf("worker VMT doc was modified when no worker fields were set:\noriginal:\n%s\ngot:\n%s", orig, docs[i])
			}
		}
	}
}
