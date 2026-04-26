package orchestrator

// Snapshot goldens for the dry-run plan output (Track D of the §19
// parallelization plan; per docs/abstraction-plan.md §14.B DoD
// "snapshot tests gate the diff" + §19.6 Track D).
//
// Each TestPlanGolden_* case captures the full output of
// PrintPlanTo against a hand-built *config.Config, then compares it
// byte-for-byte against testdata/plan/<scenario>.golden. When the
// orchestrator's plan output legitimately changes, regenerate the
// fixtures with:
//
//   UPDATE_GOLDENS=1 go test ./internal/orchestrator/ -run TestPlanGolden
//
// The goldens are checked in as plain text. A fresh test run reads
// them and compares; any drift in the rendered plan fails CI.
//
// Hermetic setup (so the goldens don't drift on day-to-day machines):
//   - pricing.SetAirgapped(true)        — no live AWS/GCP/Hetzner pricing calls
//   - YAGE_PRICING_DISABLED=true        — same idea, belt-and-suspenders
//   - YAGE_TALLER_CURRENCY=USD          — skip the geo-IP currency probe
//   - YAGE_NO_PRICING_ONBOARDING=1      — suppress the first-run IAM hint block
//   - HOME / KUBECONFIG → t.TempDir()   — keep ContextExists() and the AWS
//                                         home-dir creds probe deterministic
//   - PATH=""                           — make CommandExists() return false
//                                         for every CLI in planPhase1
//   - AWS_ACCESS_KEY_ID / HCLOUD_TOKEN  — set to dummy values so the
//                                         "Estimated monthly cost" section is
//                                         skipped silently (PricingCredsConfigured
//                                         returns true) instead of emitting an
//                                         "(skipped: …)" bullet that would
//                                         otherwise depend on the developer's box.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"

	// Provider self-registrations for tests.
	_ "github.com/lpasquali/yage/internal/provider/aws"
	_ "github.com/lpasquali/yage/internal/provider/hetzner"
	_ "github.com/lpasquali/yage/internal/provider/proxmox"
)

// goldenDir is the on-disk root for the snapshot fixtures.
const goldenDir = "testdata/plan"

// hermeticEnv neutralises every env-var / FS lookup that would
// otherwise make the dry-run plan depend on the developer's box.
// Caller is responsible for setting any scenario-specific env vars
// AFTER calling this (t.Setenv unwinds in registration order).
func hermeticEnv(t *testing.T) {
	t.Helper()
	pricing.SetAirgapped(true)
	t.Cleanup(func() { pricing.SetAirgapped(false) })

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("KUBECONFIG", filepath.Join(tmp, "kubeconfig-empty"))
	t.Setenv("PATH", "")

	t.Setenv("YAGE_PRICING_DISABLED", "true")
	t.Setenv("YAGE_TALLER_CURRENCY", "USD")
	t.Setenv("YAGE_NO_PRICING_ONBOARDING", "1")
	t.Setenv("YAGE_FORCE_PRICING_ONBOARDING", "")

	// PricingCredsConfigured("aws"|"hetzner") returns true when these
	// are non-empty — that path skips the "Estimated monthly cost"
	// section silently, which is what we want for hermetic goldens
	// (live pricing is disabled, so the estimate would always fail).
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATESTONLY")
	t.Setenv("HCLOUD_TOKEN", "test-token")
}

// baseCfg returns a minimal hermetic *config.Config with all the
// universal cluster-shape fields filled in. Callers add provider-
// specific fields and toggles.
func baseCfg() *config.Config {
	c := &config.Config{}

	c.WorkloadClusterNamespace = "default"
	c.WorkloadClusterName = "capi-quickstart"
	c.WorkloadKubernetesVersion = "v1.32.0"
	c.ControlPlaneMachineCount = "1"
	c.WorkerMachineCount = "2"
	c.ControlPlaneEndpointIP = "10.0.0.1"
	c.ControlPlaneEndpointPort = "6443"
	c.NodeIPRanges = "10.0.0.10-10.0.0.30"
	c.Gateway = "10.0.0.1"
	c.IPPrefix = "24"
	c.DNSServers = "1.1.1.1,8.8.8.8"

	// Cluster identity / naming
	c.ClusterSetID = "test-clusterset"
	c.KindClusterName = "capi-test"
	c.IPAMProvider = "in-cluster"

	// Tool versions — pinned so the golden doesn't drift with
	// upstream releases.
	c.KindVersion = "v0.24.0"
	c.KubectlVersion = "v1.32.0"
	c.ClusterctlVersion = "v1.8.5"
	c.CiliumCLIVersion = "v0.16.20"
	c.CiliumVersion = "1.16.5"
	c.ArgoCDVersion = "v2.13.2"
	c.ArgoCDOperatorVersion = "v0.12.2"
	c.KyvernoCLIVersion = "v1.13.2"
	c.CmctlVersion = "v2.1.1"
	c.OpenTofuVersion = "1.8.5"
	c.CAPMOXVersion = "v0.7.0"

	// ArgoCD on workload — defaults.
	c.ArgoCDEnabled = true
	c.WorkloadArgoCDEnabled = true
	c.WorkloadArgoCDNamespace = "argocd"
	c.WorkloadAppOfAppsGitURL = "https://github.com/example/app-of-apps"
	c.WorkloadAppOfAppsGitPath = "apps"
	c.WorkloadAppOfAppsGitRef = "main"

	// Cilium — fixed strings so the workload-section bullet is stable.
	c.CiliumKubeProxyReplacement = "true"
	c.CiliumIngress = "false"
	c.CiliumHubble = "true"
	c.CiliumLBIPAM = "true"
	c.CiliumGatewayAPIEnabled = "false"

	// Capacity / allocation knobs (deterministic numbers).
	c.ResourceBudgetFraction = 0.75
	c.OvercommitTolerancePct = 100
	c.SystemAppsCPUMillicores = 2000
	c.SystemAppsMemoryMiB = 4096

	// Pivot defaults (off; proxmox-pivot scenario flips this).
	c.PivotEnabled = false
	c.PivotVerifyTimeout = "5m"

	return c
}

// proxmoxCfg returns baseCfg + Proxmox-specific defaults. Used by
// proxmox-default and proxmox-pivot scenarios.
func proxmoxCfg() *config.Config {
	c := baseCfg()
	c.InfraProvider = "proxmox"

	px := &c.Providers.Proxmox
	px.URL = "https://proxmox.example.com:8006"
	px.Node = "pve1"
	px.TemplateID = "9000"
	px.Bridge = "vmbr0"
	px.Pool = "capi-quickstart"
	px.IdentitySuffix = "test"
	px.CAPIUserID = "capi@pve"
	px.CAPITokenPrefix = "capi-token"
	px.CSIUserID = "csi@pve"
	px.CSITokenPrefix = "csi-token"
	px.CSIEnabled = true
	px.CSIChartName = "proxmox-csi-plugin"
	px.CSIChartVersion = "0.10.0"
	px.CSINamespace = "csi-proxmox"
	px.CSIStorageClassName = "proxmox-csi"
	px.CSIDefaultClass = "true"
	px.CSIStorage = "local-lvm"

	// Per-cluster VM sizing — Proxmox-shaped numbers used both by
	// DescribeWorkload and by the central capacity / allocations
	// sections.
	px.ControlPlaneNumSockets = "1"
	px.ControlPlaneNumCores = "2"
	px.ControlPlaneMemoryMiB = "4096"
	px.ControlPlaneBootVolumeDevice = "scsi0"
	px.ControlPlaneBootVolumeSize = "30"
	px.WorkerNumSockets = "1"
	px.WorkerNumCores = "4"
	px.WorkerMemoryMiB = "8192"
	px.WorkerBootVolumeDevice = "scsi0"
	px.WorkerBootVolumeSize = "60"

	return c
}

// awsCfg returns baseCfg + AWS-specific defaults. AWS_REGION =
// us-east-1 is set on cfg.Providers.AWS.Region (the env-var loader
// path isn't exercised in unit tests).
func awsCfg() *config.Config {
	c := baseCfg()
	c.InfraProvider = "aws"

	aws := &c.Providers.AWS
	aws.Region = "us-east-1"
	aws.Mode = "unmanaged"
	aws.ControlPlaneMachineType = "t3.large"
	aws.NodeMachineType = "t3.medium"
	aws.OverheadTier = "prod"

	// AWS DescribeWorkload reuses cfg.Providers.Proxmox.{ControlPlane,Worker}BootVolumeSize
	// as a generic "boot volume" sizing knob. Set them so the bullet
	// renders deterministically.
	c.Providers.Proxmox.ControlPlaneBootVolumeSize = "30"
	c.Providers.Proxmox.WorkerBootVolumeSize = "40"

	return c
}

// hetznerCfg returns baseCfg + Hetzner-specific defaults.
func hetznerCfg() *config.Config {
	c := baseCfg()
	c.InfraProvider = "hetzner"

	h := &c.Providers.Hetzner
	h.Location = "fsn1"
	h.ControlPlaneMachineType = "cx22"
	h.NodeMachineType = "cx22"
	h.OverheadTier = "prod"

	return c
}

// captureFor runs PrintPlanTo against the given cfg and returns
// the output as a string.
func captureFor(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var buf bytes.Buffer
	PrintPlanTo(&buf, cfg)
	return buf.String()
}

// runGolden runs cfg through PrintPlanTo and either updates the
// golden (UPDATE_GOLDENS=1) or compares against it.
func runGolden(t *testing.T, name string, cfg *config.Config) {
	t.Helper()
	got := captureFor(t, cfg)
	path := filepath.Join(goldenDir, name+".golden")
	if os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", goldenDir, err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("UPDATE_GOLDENS=1: wrote %s (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (regenerate with UPDATE_GOLDENS=1): %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("plan output drift for %s\n--- want (golden)\n%s\n--- got\n%s\n--- diff (first mismatch)\n%s",
			name, string(want), got, firstDiff(string(want), got))
	}
}

// firstDiff returns a human-readable pointer to where want and got
// diverge — line number + the two lines side by side. Helps when the
// goldens drift after a refactor.
func firstDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] != gl[i] {
			return "line " + itoa(i+1) + ":\n  want: " + wl[i] + "\n  got:  " + gl[i]
		}
	}
	if len(wl) != len(gl) {
		return "length mismatch: want " + itoa(len(wl)) + " lines, got " + itoa(len(gl)) + " lines"
	}
	return "<no diff?>"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	bp := len(b)
	for i > 0 {
		bp--
		b[bp] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		bp--
		b[bp] = '-'
	}
	return string(b[bp:])
}

// TestPlanGolden_ProxmoxDefault — INFRA_PROVIDER=proxmox + minimal
// cfg. Pivot disabled.
func TestPlanGolden_ProxmoxDefault(t *testing.T) {
	hermeticEnv(t)
	cfg := proxmoxCfg()
	runGolden(t, "proxmox-default", cfg)
}

// TestPlanGolden_AWSDefault — INFRA_PROVIDER=aws + AWS_REGION=us-east-1,
// mode=unmanaged.
func TestPlanGolden_AWSDefault(t *testing.T) {
	hermeticEnv(t)
	cfg := awsCfg()
	runGolden(t, "aws-default", cfg)
}

// TestPlanGolden_HetznerDefault — INFRA_PROVIDER=hetzner +
// HCLOUD_REGION=fsn1.
func TestPlanGolden_HetznerDefault(t *testing.T) {
	hermeticEnv(t)
	cfg := hetznerCfg()
	runGolden(t, "hetzner-default", cfg)
}

// TestPlanGolden_AWSEKS — same as AWSDefault but with AWS_MODE=eks.
// Confirms the workload section header changes ("mode: eks") and
// the cost-section item flips to the EKS managed CP line — though
// in airgapped tests we only assert the section header diff (cost
// is skipped per hermeticEnv).
func TestPlanGolden_AWSEKS(t *testing.T) {
	hermeticEnv(t)
	cfg := awsCfg()
	cfg.Providers.AWS.Mode = "eks"
	runGolden(t, "aws-eks", cfg)
}

// TestPlanGolden_ProxmoxPivot — INFRA_PROVIDER=proxmox with
// PIVOT_ENABLED=true; the Pivot section grows real bullets and the
// capacity plan adds mgmt control-plane / mgmt worker rows.
func TestPlanGolden_ProxmoxPivot(t *testing.T) {
	hermeticEnv(t)
	cfg := proxmoxCfg()
	cfg.PivotEnabled = true

	c := &cfg.Mgmt
	c.ClusterName = "yage-mgmt"
	c.ClusterNamespace = "yage-system"
	c.KubernetesVersion = "v1.32.0"
	c.ControlPlaneMachineCount = "1"
	c.WorkerMachineCount = "0"
	c.ControlPlaneEndpointIP = "10.0.0.2"
	c.ControlPlaneEndpointPort = "6443"
	c.NodeIPRanges = "10.0.0.40-10.0.0.41"
	c.CiliumHubble = "true"
	c.CiliumLBIPAM = "false"

	mgmt := &cfg.Providers.Proxmox.Mgmt
	mgmt.ControlPlaneNumSockets = "1"
	mgmt.ControlPlaneNumCores = "2"
	mgmt.ControlPlaneMemoryMiB = "4096"
	mgmt.ControlPlaneBootVolumeSize = "30"
	mgmt.Pool = "yage-mgmt"
	mgmt.CSIEnabled = false

	runGolden(t, "proxmox-pivot", cfg)
}
