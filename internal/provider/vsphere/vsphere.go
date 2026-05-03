// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package vsphere is the yage Provider implementation for
// the Cluster API vSphere infrastructure provider (CAPV —
// github.com/kubernetes-sigs/cluster-api-provider-vsphere).
//
// Shape: closer to Proxmox than to CAPD, but with identity handled
// out-of-band. The vSphere admin pre-creates a service account with
// the CAPV-required role bindings; yage consumes the
// resulting credentials via the standard CAPV env vars
// (VSPHERE_USERNAME / VSPHERE_PASSWORD / VSPHERE_SERVER) which the
// orchestrator passes through to clusterctl.
//
// EnsureGroup creates the VM folder (VSPHERE_FOLDER) via govmomi.
// Inventory surfaces per-ResourcePool CPU/memory/storage capacity
// using govmomi property collection; returns ErrNotApplicable when
// the pool has unlimited CPU or memory limits (MaxUsage == -1).
//
// The K3s template targets VSphereCluster + VSphereMachineTemplate
// (apiVersion infrastructure.cluster.x-k8s.io/v1beta1 — CAPV is one
// version behind the v1beta2 the rest of CAPI is on). The
// MachineTemplate spec carries inline sizing fields (numCPUs,
// numCoresPerSocket, memoryMiB, diskGiB) that map to
// cfg.Providers.Vsphere.*NumCPUs / *MemoryMiB / *DiskGiB. PatchManifest
// rewrites them post-render when the corresponding config fields are set.
package vsphere

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("vsphere", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPV.
type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "vsphere" }
func (p *Provider) InfraProviderName() string { return "vsphere" }

// EnsureIdentity is a no-op for vSphere: the vSphere admin creates
// the CAPV service account + role bindings out-of-band, and the
// resulting credentials are supplied via VSPHERE_USERNAME /
// VSPHERE_PASSWORD / VSPHERE_SERVER env vars at run time.
func (p *Provider) EnsureIdentity(cfg *config.Config) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure vsphere".
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "vsphere"}
}

// k3sTemplate is the vSphere-flavored K3s manifest. Mirrors the
// Proxmox K3s flow: KThreesControlPlane references a
// VSphereMachineTemplate; MachineDeployment references a separate
// VSphereMachineTemplate plus a KThreesConfigTemplate. CAPV's CRDs
// are still on infrastructure.cluster.x-k8s.io/v1beta1 (the rest of
// the manifest stays on the v1beta2 cluster + bootstrap +
// controlplane groups).
const k3sTemplate = `apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  clusterNetwork:
    pods:
      cidrBlocks: ["192.168.0.0/16"]
    services:
      cidrBlocks: ["10.96.0.0/12"]
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta2
    kind: KThreesControlPlane
    name: ${CLUSTER_NAME}-control-plane
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: VSphereCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  server: ${VSPHERE_SERVER}
  thumbprint: ${VSPHERE_TLS_THUMBPRINT}
  controlPlaneEndpoint:
    host: ${CONTROL_PLANE_ENDPOINT_IP}
    port: ${CONTROL_PLANE_ENDPOINT_PORT}
  identityRef:
    kind: Secret
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      cloneMode: linkedClone
      datacenter: ${VSPHERE_DATACENTER}
      datastore: ${VSPHERE_DATASTORE}
      diskGiB: 25
      folder: ${VSPHERE_FOLDER}
      memoryMiB: ${CONTROL_PLANE_MEMORY_MIB}
      network:
        devices:
        - dhcp4: true
          networkName: ${VSPHERE_NETWORK}
      numCPUs: ${CONTROL_PLANE_NUM_CORES}
      numCoresPerSocket: 1
      os: Linux
      resourcePool: ${VSPHERE_RESOURCE_POOL}
      server: ${VSPHERE_SERVER}
      storagePolicyName: ""
      template: ${VSPHERE_TEMPLATE}
      thumbprint: ${VSPHERE_TLS_THUMBPRINT}
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta2
kind: KThreesControlPlane
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
  version: ${KUBERNETES_VERSION}+k3s1
  machineTemplate:
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: VSphereMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
      - "--cloud-provider=external"
    files:
    - path: /var/lib/rancher/k3s/server/manifests/coredns-config.yaml
      owner: root:root
      permissions: "0644"
      content: ""
    preK3sCommands: []
    users:
    - name: capv
      sshAuthorizedKeys:
      - ${VM_SSH_KEYS}
      sudo: ALL=(ALL) NOPASSWD:ALL
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineDeployment
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  clusterName: ${CLUSTER_NAME}
  replicas: ${WORKER_MACHINE_COUNT}
  selector: { matchLabels: {} }
  template:
    spec:
      clusterName: ${CLUSTER_NAME}
      version: ${KUBERNETES_VERSION}+k3s1
      bootstrap:
        configRef:
          apiVersion: orchestrator.cluster.x-k8s.io/v1beta2
          kind: KThreesConfigTemplate
          name: ${CLUSTER_NAME}-md-0
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: VSphereMachineTemplate
        name: ${CLUSTER_NAME}-md-0
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      cloneMode: linkedClone
      datacenter: ${VSPHERE_DATACENTER}
      datastore: ${VSPHERE_DATASTORE}
      diskGiB: 25
      folder: ${VSPHERE_FOLDER}
      memoryMiB: ${WORKER_MEMORY_MIB}
      network:
        devices:
        - dhcp4: true
          networkName: ${VSPHERE_NETWORK}
      numCPUs: ${WORKER_NUM_CORES}
      numCoresPerSocket: 1
      os: Linux
      resourcePool: ${VSPHERE_RESOURCE_POOL}
      server: ${VSPHERE_SERVER}
      storagePolicyName: ""
      template: ${VSPHERE_TEMPLATE}
      thumbprint: ${VSPHERE_TLS_THUMBPRINT}
---
apiVersion: orchestrator.cluster.x-k8s.io/v1beta2
kind: KThreesConfigTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      agentConfig:
        airGapped: false
        kubeletArgs:
        - "--cloud-provider=external"
      users:
      - name: capv
        sshAuthorizedKeys:
        - ${VM_SSH_KEYS}
        sudo: ALL=(ALL) NOPASSWD:ALL
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest rewrites the VSphereMachineTemplate sizing fields
// (numCPUs, numCoresPerSocket, memoryMiB, diskGiB) in the rendered
// manifest at manifestPath.
//
// Role detection: a template whose metadata.name contains "control-plane"
// receives CP sizing; all other VSphereMachineTemplate documents receive
// worker sizing. This matches the naming convention in K3sTemplate:
// "${CLUSTER_NAME}-control-plane" and "${CLUSTER_NAME}-md-0".
//
// Only non-empty cfg.Providers.Vsphere.* fields are patched; an empty
// value means "leave whatever the template rendered". mgmt=true is a
// no-op (the vSphere path does not pivot to a vSphere mgmt cluster today).
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	if mgmt {
		return nil
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	text := string(raw)
	vs := cfg.Providers.Vsphere

	type sizing struct {
		numCPUs           string
		numCoresPerSocket string
		memoryMiB         string
		diskGiB           string
	}
	cp := sizing{vs.ControlPlaneNumCPUs, vs.ControlPlaneNumCoresPerSocket, vs.ControlPlaneMemoryMiB, vs.ControlPlaneDiskGiB}
	wk := sizing{vs.WorkerNumCPUs, vs.WorkerNumCoresPerSocket, vs.WorkerMemoryMiB, vs.WorkerDiskGiB}

	// Nothing to patch if all fields are empty.
	if cp == (sizing{}) && wk == (sizing{}) {
		return nil
	}

	// Line-anchored regex to identify VSphereMachineTemplate documents.
	// Never use strings.Contains: infrastructureRef blocks embed the same
	// kind string nested inside other documents.
	vmtKindRE := regexp.MustCompile(`(?m)^kind:\s*VSphereMachineTemplate\s*$`)
	nameRE := regexp.MustCompile(`(?m)^  name:\s*(\S+)\s*$`)

	parts := strings.Split(text, "\n---\n")
	for i, doc := range parts {
		if !vmtKindRE.MatchString(doc) {
			continue
		}
		// Determine role from metadata.name.
		m := nameRE.FindStringSubmatch(doc)
		if m == nil {
			continue
		}
		var sz sizing
		if strings.Contains(m[1], "control-plane") {
			sz = cp
		} else {
			sz = wk
		}
		if sz == (sizing{}) {
			continue
		}
		if sz.numCPUs != "" {
			doc = replaceFirstField(doc, "numCPUs", sz.numCPUs)
		}
		if sz.numCoresPerSocket != "" {
			doc = replaceFirstField(doc, "numCoresPerSocket", sz.numCoresPerSocket)
		}
		if sz.memoryMiB != "" {
			doc = replaceFirstField(doc, "memoryMiB", sz.memoryMiB)
		}
		if sz.diskGiB != "" {
			doc = replaceFirstField(doc, "diskGiB", sz.diskGiB)
		}
		parts[i] = doc
	}

	out := strings.Join(parts, "\n---\n")
	if out == text {
		return nil
	}
	return os.WriteFile(manifestPath, []byte(out), 0o644)
}

// replaceFirstField replaces the value of a YAML scalar field within a
// single document. Matches `<key>: <anything>` at any indentation and
// replaces only the first occurrence (there is exactly one of each
// sizing field per VSphereMachineTemplate document).
func replaceFirstField(doc, key, value string) string {
	re := regexp.MustCompile(`(?m)^([ \t]*` + regexp.QuoteMeta(key) + `:\s*)(\S+)`)
	replaced := false
	return re.ReplaceAllStringFunc(doc, func(match string) string {
		if replaced {
			return match
		}
		replaced = true
		sub := re.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		return sub[1] + value
	})
}

// EstimateMonthlyCostUSD — vSphere is self-hosted; vendor licensing
// is enterprise-quote-only, so there's no public pricing API. The
// operator opts into a TCO estimate by passing --hardware-cost-usd
// (and optionally --hardware-watts / --hardware-kwh-rate-usd /
// --hardware-support-usd-month for vSphere licensing/support).
// Without those, returns ErrNotApplicable.
func (p *Provider) EstimateMonthlyCostUSD(_ context.Context, cfg *config.Config) (provider.CostEstimate, error) {
	return provider.TCOEstimate(cfg, "vsphere")
}
