// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package installer ports install_binary and every ensure_* helper from
// the original bash port (lines ~1994-2858). These functions install or upgrade
// third-party CLIs to pinned versions, mirroring the bash behavior:
//
//   - If the binary is missing, install.
//   - If the binary is present and versionx.Match reports the pinned version,
//     do nothing.
//   - Otherwise warn about the mismatch and reinstall.
//
// The underlying download primitive is installBinary, equivalent to:
//
//	install_binary <name> <url>
//
// which curls the URL into /tmp and then `install`s it into /usr/local/bin
// with the privilege-escalation helper.
package installer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
	"github.com/lpasquali/yage/internal/util/versionx"
)

// installBinary downloads url into a temp file then installs it into
// /usr/local/bin/<name> with mode 0755, using sudo if required.
// Mirrors install_binary() in bash.
func installBinary(name, url string) error {
	logx.Log("Installing %s...", name)
	tmp := filepath.Join(os.TempDir(), name+".bin")
	if err := downloadTo(url, tmp); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer os.Remove(tmp)
	dest := filepath.Join("/usr/local/bin", name)
	return shell.RunPrivileged("install", "-m", "0755", tmp, dest)
}

// downloadTo fetches url with a single HTTP GET, writing to path atomically
// (via a .partial swap). Matches curl -fsSL behavior.
func downloadTo(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	part := path + ".partial"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, path)
}

// installTarballMember fetches url (a .tar.gz) and extracts the single
// `member` into /usr/local/bin via sudo tar. Equivalent to:
//
//	curl -fsSL URL | sudo tar -xz -C /usr/local/bin MEMBER
//
// tarStripComponents lets callers strip a leading directory component when
// the tarball wraps its binary in one.
func installTarballMember(url, member string, tarStripComponents int) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	argv := []string{"tar", "-xz", "-C", "/usr/local/bin"}
	if tarStripComponents > 0 {
		argv = append(argv, fmt.Sprintf("--strip-components=%d", tarStripComponents))
	}
	argv = append(argv, member)
	argv = shell.Privileged(argv...)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = resp.Body
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return shell.RunPrivileged("chmod", "+x", filepath.Join("/usr/local/bin", filepath.Base(member)))
}

// Kind is a no-op: cluster lifecycle is now driven through the embedded
// `sigs.k8s.io/kind/pkg/cluster` library, so the kind CLI binary is no longer
// required at runtime. The function is retained (rather than removed) to keep
// existing call sites in orchestrator.go compiling without churn. The cfg
// parameter is intentionally unused.
func Kind(cfg *config.Config) error {
	_ = cfg
	logx.Log("kind library is embedded — skipping CLI install")
	return nil
}

// Kubectl mirrors ensure_kubectl(): installs from dl.k8s.io.
func Kubectl(cfg *config.Config) error {
	if shell.CommandExists("kubectl") {
		have := kubectlClientGitVersion()
		if versionx.Match(have, cfg.KubectlVersion) {
			return nil
		}
		logx.Warn("kubectl (%s) does not match KUBECTL_VERSION=%s — reinstalling...", orUnknown(have), cfg.KubectlVersion)
	} else {
		logx.Warn("kubectl not found — installing...")
	}
	url := fmt.Sprintf(
		"https://dl.k8s.io/release/%s/bin/%s/%s/kubectl",
		cfg.KubectlVersion, sysinfo.OS(), sysinfo.Arch(),
	)
	return installBinary("kubectl", url)
}

// Clusterctl mirrors ensure_clusterctl().
func Clusterctl(cfg *config.Config) error {
	if shell.CommandExists("clusterctl") {
		have := clusterctlGitVersion()
		if versionx.Match(have, cfg.ClusterctlVersion) {
			return nil
		}
		logx.Warn("clusterctl (%s) does not match CLUSTERCTL_VERSION=%s — reinstalling...", orUnknown(have), cfg.ClusterctlVersion)
	} else {
		logx.Warn("clusterctl not found — installing...")
	}
	url := fmt.Sprintf(
		"https://github.com/kubernetes-sigs/cluster-api/releases/download/%s/clusterctl-%s-%s",
		cfg.ClusterctlVersion, sysinfo.OS(), sysinfo.Arch(),
	)
	return installBinary("clusterctl", url)
}

// CiliumCLI mirrors ensure_cilium_cli().
func CiliumCLI(cfg *config.Config) error {
	if shell.CommandExists("cilium") {
		have := firstVersionOn("cilium", "version", "2>&1")
		if versionx.Match(have, cfg.CiliumCLIVersion) {
			return nil
		}
		logx.Warn("cilium CLI (%s) does not match CILIUM_CLI_VERSION=%s — reinstalling...", orUnknown(have), cfg.CiliumCLIVersion)
	} else {
		logx.Warn("cilium CLI not found — installing...")
	}
	tarball := fmt.Sprintf("cilium-%s-%s.tar.gz", sysinfo.OS(), sysinfo.Arch())
	url := fmt.Sprintf("https://github.com/cilium/cilium-cli/releases/download/%s/%s", cfg.CiliumCLIVersion, tarball)
	if err := installTarballMember(url, "cilium", 0); err != nil {
		logx.Die("Failed to install cilium CLI (curl or tar: check CILIUM_CLI_VERSION=%s and network).", cfg.CiliumCLIVersion)
	}
	return nil
}

// ArgoCDCLI mirrors ensure_argocd_cli(). Linux-only.
func ArgoCDCLI(cfg *config.Config) error {
	if runtime.GOOS != "linux" {
		logx.Die("argocd CLI install is supported on Linux only (amd64/arm64), not %s.", runtime.GOOS)
	}
	if shell.CommandExists("argocd") {
		have := firstVersionOn("argocd", "version", "--client", "2>&1")
		if versionx.Match(have, cfg.ArgoCDVersion) {
			return nil
		}
		logx.Warn("argocd CLI (%s) does not match ARGOCD_VERSION=%s — reinstalling...", orUnknown(have), cfg.ArgoCDVersion)
	} else {
		logx.Warn("argocd CLI not found — installing...")
	}
	arch := sysinfo.Arch()
	switch arch {
	case "amd64", "arm64":
	default:
		logx.Die("Unsupported architecture for argocd CLI on Linux: %s (need amd64 or arm64).", arch)
	}
	url := fmt.Sprintf("https://github.com/argoproj/argo-cd/releases/download/%s/argocd-linux-%s", cfg.ArgoCDVersion, arch)
	return installBinary("argocd", url)
}

// KyvernoCLI mirrors ensure_kyverno_cli(). Linux-only; amd64 uses x86_64 in
// the asset name.
func KyvernoCLI(cfg *config.Config) error {
	if runtime.GOOS != "linux" {
		logx.Die("kyverno CLI install is supported on Linux only (amd64/arm64), not %s.", runtime.GOOS)
	}
	if shell.CommandExists("kyverno") {
		have := firstVersionOn("kyverno", "version", "2>&1")
		if versionx.Match(have, cfg.KyvernoCLIVersion) {
			return nil
		}
		logx.Warn("kyverno CLI (%s) does not match KYVERNO_CLI_VERSION=%s — reinstalling...", orUnknown(have), cfg.KyvernoCLIVersion)
	} else {
		logx.Warn("kyverno CLI not found — installing...")
	}
	var kyArch string
	switch sysinfo.Arch() {
	case "amd64":
		kyArch = "x86_64"
	case "arm64":
		kyArch = "arm64"
	default:
		logx.Die("Unsupported architecture for kyverno CLI on Linux: %s (need amd64 or arm64).", sysinfo.Arch())
	}
	tarball := fmt.Sprintf("kyverno-cli_%s_linux_%s.tar.gz", cfg.KyvernoCLIVersion, kyArch)
	url := fmt.Sprintf("https://github.com/kyverno/kyverno/releases/download/%s/%s", cfg.KyvernoCLIVersion, tarball)
	if err := installTarballMember(url, "kyverno", 0); err != nil {
		logx.Die("Failed to install kyverno CLI (check KYVERNO_CLI_VERSION=%s and network).", cfg.KyvernoCLIVersion)
	}
	return nil
}

// Cmctl mirrors ensure_cmctl(). Linux-only.
func Cmctl(cfg *config.Config) error {
	if runtime.GOOS != "linux" {
		logx.Die("cmctl install is supported on Linux only (amd64/arm64), not %s.", runtime.GOOS)
	}
	if shell.CommandExists("cmctl") {
		have := firstVersionOn("cmctl", "version", "2>&1")
		if versionx.Match(have, cfg.CmctlVersion) {
			return nil
		}
		logx.Warn("cmctl (%s) does not match CMCTL_VERSION=%s — reinstalling...", orUnknown(have), cfg.CmctlVersion)
	} else {
		logx.Warn("cmctl (cert-manager) not found — installing...")
	}
	arch := sysinfo.Arch()
	if arch != "amd64" && arch != "arm64" {
		logx.Die("Unsupported architecture for cmctl on Linux: %s (need amd64 or arm64).", arch)
	}
	tarball := fmt.Sprintf("cmctl_linux_%s.tar.gz", arch)
	url := fmt.Sprintf("https://github.com/cert-manager/cmctl/releases/download/%s/%s", cfg.CmctlVersion, tarball)
	if err := installTarballMember(url, "cmctl", 0); err != nil {
		logx.Die("Failed to install cmctl (check CMCTL_VERSION=%s and network).", cfg.CmctlVersion)
	}
	return nil
}

// SystemDependencies mirrors ensure_system_dependencies(): git, curl, python3.
// Python3 is still required for the unported bash→Python inline scripts; as
// those get ported over, python3 can be removed from this list.
func SystemDependencies() error {
	logx.Log("Checking and installing system-wide dependencies...")
	for _, p := range []string{"git", "curl", "python3"} {
		if shell.CommandExists(p) {
			continue
		}
		logx.Warn("%s not found — installing...", p)
		if err := installSystemPackage(p); err != nil {
			return err
		}
	}
	logx.Log("System-wide dependencies check complete.")
	return nil
}

// Helm installs the helm CLI for end-user interaction with the
// deployed CAPI infrastructure. The bootstrap itself drives Helm
// via `helm.sh/helm/v3` in-process; this binary is shipped for
// users so they can `helm list` / `helm install` against their
// workload cluster after the orchestrator finishes. Fetches the
// upstream get-helm-3 script and runs it under the privilege helper.
func Helm() error {
	if shell.CommandExists("helm") {
		return nil
	}
	logx.Warn("helm CLI not found — installing via https://get.helm.sh/...")
	body, err := fetchAll("https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3")
	if err != nil {
		return fmt.Errorf("fetch get-helm-3: %w", err)
	}
	if err := shell.RunPrivileged("bash", "-c", body); err != nil {
		logx.Die("helm installation failed: %v", err)
	}
	if !shell.CommandExists("helm") {
		logx.Die("helm installation failed.")
	}
	return nil
}

// Docker installs Docker Engine when it is not already on PATH
// (curl get.docker.com, optional usermod, systemctl enable --now
// docker).
func Docker() {
	if shell.CommandExists("docker") {
		v, _, _ := shell.Capture("docker", "--version")
		logx.Log("Docker already installed (%s).", strings.TrimSpace(v))
		return
	}
	logx.Log("Docker not found — installing via get.docker.com...")
	body, err := fetchAll("https://get.docker.com")
	if err != nil {
		logx.Die("Failed to fetch Docker installer: %v", err)
	}
	// Pipe the script through bash (privileged).
	if err := shell.RunPrivileged("bash", "-c", body); err != nil {
		logx.Die("Docker installation failed.")
	}
	// Optionally add SUDO_USER to the docker group.
	if u := os.Getenv("SUDO_USER"); u != "" {
		_ = shell.RunPrivileged("usermod", "-aG", "docker", u)
	}
	if shell.CommandExists("systemctl") {
		_ = shell.RunPrivileged("systemctl", "enable", "--now", "docker")
	}
	if !shell.CommandExists("docker") {
		logx.Die("Docker installation failed.")
	}
	logx.Log("Docker installed successfully.")
}

// UpgradeDocker upgrades docker-ce / docker-ce-cli / containerd.io
// via the host package manager. Best-effort: unknown package
// managers are warned about and skipped.
func UpgradeDocker() {
	switch {
	case shell.CommandExists("apt-get"):
		_ = shell.RunPrivileged("apt-get", "update", "-qq")
		_ = shell.RunPrivileged("apt-get", "install", "-y", "--only-upgrade",
			"docker-ce", "docker-ce-cli", "containerd.io")
	case shell.CommandExists("dnf"):
		_ = shell.RunPrivileged("dnf", "upgrade", "-y",
			"docker-ce", "docker-ce-cli", "containerd.io")
	case shell.CommandExists("yum"):
		_ = shell.RunPrivileged("yum", "update", "-y",
			"docker-ce", "docker-ce-cli", "containerd.io")
	default:
		logx.Warn("Unknown package manager — skipping Docker update.")
	}
}

// fetchAll is a minimal `curl -fsSL URL` used by the Docker installer.
// Kept here so it doesn't multiply across packages.
func fetchAll(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// HasArm64Image reports whether an image has an arm64 variant in its
// Docker manifest list. Replaces the bash `docker manifest inspect |
// python3` pipeline with native Go JSON parsing. Returns false on any
// error (docker missing, network, image absent).
func HasArm64Image(image string) bool {
	if !shell.CommandExists("docker") {
		return false
	}
	out, _, err := shell.Capture("docker", "manifest", "inspect", image)
	if err != nil || out == "" {
		return false
	}
	type entry struct {
		Platform struct {
			Architecture string `json:"architecture"`
		} `json:"platform"`
	}
	var m struct {
		Manifests    []entry `json:"manifests"`
		ManifestsAlt []entry `json:"Manifests"`
	}
	_ = json.Unmarshal([]byte(out), &m)
	for _, x := range append(m.Manifests, m.ManifestsAlt...) {
		if x.Platform.Architecture == "arm64" {
			return true
		}
	}
	return false
}

// BuildIfNoArm64 ports build_if_no_arm64(). If an arm64 variant exists in
// the registry (and BUILD_ALL is false), returns nil without building —
// the registry image is used. Otherwise clones repo @ tag into dir,
// docker-builds image, and loads it into the named kind cluster.
func BuildIfNoArm64(cfg *config.Config, image, repo, tag, dir, cluster string) error {
	if cluster == "" {
		cluster = cfg.KindClusterName
	}
	if !cfg.BuildAll {
		if HasArm64Image(image) {
			logx.Log("arm64 image available in registry: %s — skipping local pull/build/load.", image)
			return nil
		}
		logx.Warn("No arm64 image found for %s — building from source...", image)
	} else {
		logx.Warn("BUILD_ALL enabled — building %s from source even though a registry image may exist.", image)
	}
	_ = os.RemoveAll(dir)
	if err := shell.Run("git", "clone", "--filter=blob:none", "--branch", tag, "--depth", "1", repo, dir); err != nil {
		return err
	}
	if err := shell.Run("docker", "build", "-t", image, dir); err != nil {
		return err
	}
	logx.Log("Loading locally built image %s into kind cluster '%s'...", image, cluster)
	return shell.Run("kind", "load", "docker-image", image, "--name", cluster)
}

// OpenTofu mirrors ensure_opentofu(): downloads the OpenTofu release zip
// and extracts the `tofu` binary into /usr/local/bin.
//
// `tofu version -json` emits the same schema Terraform did, including the
// `terraform_version` key — we reuse the existing parser for that reason.
func OpenTofu(cfg *config.Config) error {
	if shell.CommandExists("tofu") {
		have := tofuJSONVersion()
		if versionx.Match(have, cfg.OpenTofuVersion) {
			return nil
		}
		logx.Warn("tofu (%s) does not match OPENTOFU_VERSION=%s — reinstalling...", orUnknown(have), cfg.OpenTofuVersion)
	} else {
		logx.Warn("tofu not found — installing...")
	}
	osName := sysinfo.OS()
	arch := sysinfo.Arch()
	url := fmt.Sprintf("https://github.com/opentofu/opentofu/releases/download/v%s/tofu_%s_%s_%s.zip",
		cfg.OpenTofuVersion, cfg.OpenTofuVersion, osName, arch)
	zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("tofu_%s_%s_%s.zip", cfg.OpenTofuVersion, osName, arch))
	if err := downloadTo(url, zipPath); err != nil {
		logx.Die("Failed to download OpenTofu from %s (check OPENTOFU_VERSION=%s and network).", url, cfg.OpenTofuVersion)
	}
	defer os.Remove(zipPath)
	bin := filepath.Join(os.TempDir(), "tofu.bin")
	if err := extractZipMember(zipPath, "tofu", bin); err != nil {
		return err
	}
	defer os.Remove(bin)
	return shell.RunPrivileged("install", "-m", "0755", bin, "/usr/local/bin/tofu")
}

// --- helpers ---

// firstVersionOn runs `name args...` and extracts the first "vX.Y.Z"-looking
// token from stdout+stderr, matching the bash idiom:
//
//	cmd --version 2>&1 | grep -oE 'v?[0-9][0-9.]+' | head -1
func firstVersionOn(name string, args ...string) string {
	// strip the 2>&1 shell sentinel if present in args
	cleaned := make([]string, 0, len(args))
	for _, a := range args {
		if a == "2>&1" {
			continue
		}
		cleaned = append(cleaned, a)
	}
	c := exec.Command(name, cleaned...)
	out, _ := c.CombinedOutput()
	return firstSemverish(string(out))
}

func firstSemverish(s string) string {
	// Match "v?[0-9][0-9.]+" non-greedy; Go's regexp is fine here but we
	// implement a tiny scanner to avoid the dependency cost.
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 'v' && i+1 < len(s) && isDigit(s[i+1]) {
			return readSemver(s[i:])
		}
		if isDigit(c) {
			return readSemver(s[i:])
		}
	}
	return ""
}

func readSemver(s string) string {
	end := 0
	if s[0] == 'v' {
		end = 1
	}
	for end < len(s) {
		c := s[end]
		if !(isDigit(c) || c == '.') {
			break
		}
		end++
	}
	return s[:end]
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func kubectlClientGitVersion() string {
	out, _, _ := shell.Capture("kubectl", "version", "-o", "json")
	var d struct {
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
	}
	_ = json.Unmarshal([]byte(out), &d)
	return d.ClientVersion.GitVersion
}

func clusterctlGitVersion() string {
	out, _, _ := shell.Capture("clusterctl", "version", "-o", "json")
	// clusterctl v1.13+ wraps the version under a top-level "clusterctl"
	// key; older releases used "clientVersion" or "ClientVersion".
	var d struct {
		Clusterctl struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clusterctl"`
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
		ClientVersionCap struct {
			GitVersion string `json:"GitVersion"`
		} `json:"ClientVersion"`
	}
	_ = json.Unmarshal([]byte(out), &d)
	if d.Clusterctl.GitVersion != "" {
		return d.Clusterctl.GitVersion
	}
	if d.ClientVersion.GitVersion != "" {
		return d.ClientVersion.GitVersion
	}
	return d.ClientVersionCap.GitVersion
}

func tofuJSONVersion() string {
	out, _, _ := shell.Capture("tofu", "version", "-json")
	var d struct {
		// OpenTofu emits `terraform_version` in this schema for
		// compatibility; that's the key we parse.
		TerraformVersion string `json:"terraform_version"`
	}
	_ = json.Unmarshal([]byte(out), &d)
	return d.TerraformVersion
}

func orUnknown(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return v
}

// installSystemPackage mirrors the apt-get/dnf/yum/apk branching from
// ensure_system_dependencies for a single package name.
func installSystemPackage(pkg string) error {
	switch {
	case shell.CommandExists("apt-get"):
		if err := shell.RunPrivileged("apt-get", "update", "-qq"); err != nil {
			return err
		}
		return shell.RunPrivileged("apt-get", "install", "-y", pkg)
	case shell.CommandExists("dnf"):
		return shell.RunPrivileged("dnf", "install", "-y", pkg)
	case shell.CommandExists("yum"):
		return shell.RunPrivileged("yum", "install", "-y", pkg)
	case shell.CommandExists("apk"):
		return shell.RunPrivileged("apk", "add", pkg)
	}
	logx.Die("%s not found and package manager not detected — install %s manually.", pkg, pkg)
	return nil
}

// extractZipMember opens a local zip and writes member `name` to `dest`.
// Used by Terraform() — keeps the dependency-free posture of this module.
func extractZipMember(zipPath, name, dest string) error {
	// Use the standard library archive/zip via os/exec-free path. Since Go
	// supplies archive/zip, this is trivial; adding import here keeps the
	// function self-contained for readability.
	return extractZipMemberImpl(zipPath, name, dest)
}