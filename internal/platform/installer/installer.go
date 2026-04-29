// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package installer provides install_binary and the ensure_* helpers
// that install or upgrade third-party CLIs to pinned versions:
//
//   - If the binary is missing, install.
//   - If the binary is present and versionx.Match reports the pinned version,
//     do nothing.
//   - Otherwise warn about the mismatch and reinstall.
//
// The underlying download primitive is installBinary: it curls the URL
// into /tmp and then `install`s it into /usr/local/bin with the
// privilege-escalation helper.
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

// Kubectl ensures kubectl matches cfg.KubectlVersion, installing from
// dl.k8s.io when missing or out of date.
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

// Clusterctl ensures clusterctl matches cfg.ClusterctlVersion,
// installing the upstream release binary when missing or out of date.
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

// CiliumCLI ensures cilium CLI matches cfg.CiliumCLIVersion,
// installing the upstream tarball when missing or out of date.
func CiliumCLI(cfg *config.Config) error {
	if err := ciliumCLI(cfg); err != nil {
		logx.Die("Failed to install cilium CLI (curl or tar: check CILIUM_CLI_VERSION=%s and network).", cfg.CiliumCLIVersion)
	}
	return nil
}

func ciliumCLI(cfg *config.Config) error {
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
		return fmt.Errorf("install cilium CLI: %w", err)
	}
	return nil
}

// ArgoCDCLI ensures the argocd CLI matches cfg.ArgoCD.Version,
// installing the upstream release binary when missing or out of date.
// Linux-only.
func ArgoCDCLI(cfg *config.Config) error {
	if err := argoCDCLI(cfg); err != nil {
		logx.Die("%v", err)
	}
	return nil
}

func argoCDCLI(cfg *config.Config) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("argocd CLI install is supported on Linux only (amd64/arm64), not %s", runtime.GOOS)
	}
	if shell.CommandExists("argocd") {
		have := firstVersionOn("argocd", "version", "--client", "2>&1")
		if versionx.Match(have, cfg.ArgoCD.Version) {
			return nil
		}
		logx.Warn("argocd CLI (%s) does not match ARGOCD_VERSION=%s — reinstalling...", orUnknown(have), cfg.ArgoCD.Version)
	} else {
		logx.Warn("argocd CLI not found — installing...")
	}
	arch := sysinfo.Arch()
	switch arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("unsupported architecture for argocd CLI on Linux: %s (need amd64 or arm64)", arch)
	}
	url := fmt.Sprintf("https://github.com/argoproj/argo-cd/releases/download/%s/argocd-linux-%s", cfg.ArgoCD.Version, arch)
	return installBinary("argocd", url)
}

// KyvernoCLI ensures the kyverno CLI matches cfg.KyvernoCLIVersion.
// Linux-only; amd64 uses x86_64 in the asset name.
func KyvernoCLI(cfg *config.Config) error {
	if err := kyvernoCLI(cfg); err != nil {
		logx.Die("%v", err)
	}
	return nil
}

func kyvernoCLI(cfg *config.Config) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("kyverno CLI install is supported on Linux only (amd64/arm64), not %s", runtime.GOOS)
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
		return fmt.Errorf("unsupported architecture for kyverno CLI on Linux: %s (need amd64 or arm64)", sysinfo.Arch())
	}
	tarball := fmt.Sprintf("kyverno-cli_%s_linux_%s.tar.gz", cfg.KyvernoCLIVersion, kyArch)
	url := fmt.Sprintf("https://github.com/kyverno/kyverno/releases/download/%s/%s", cfg.KyvernoCLIVersion, tarball)
	if err := installTarballMember(url, "kyverno", 0); err != nil {
		return fmt.Errorf("install kyverno CLI: %w", err)
	}
	return nil
}

// Cmctl ensures the cmctl (cert-manager) CLI matches
// cfg.CmctlVersion. Linux-only.
func Cmctl(cfg *config.Config) error {
	if err := cmctlInner(cfg); err != nil {
		logx.Die("%v", err)
	}
	return nil
}

func cmctlInner(cfg *config.Config) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("cmctl install is supported on Linux only (amd64/arm64), not %s", runtime.GOOS)
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
		return fmt.Errorf("unsupported architecture for cmctl on Linux: %s (need amd64 or arm64)", arch)
	}
	tarball := fmt.Sprintf("cmctl_linux_%s.tar.gz", arch)
	url := fmt.Sprintf("https://github.com/cert-manager/cmctl/releases/download/%s/%s", cfg.CmctlVersion, tarball)
	if err := installTarballMember(url, "cmctl", 0); err != nil {
		return fmt.Errorf("install cmctl: %w", err)
	}
	return nil
}

// SystemDependencies installs the host system packages yage needs:
// git, curl, python3.
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
// Docker manifest list. Wraps `docker manifest inspect` with native Go
// JSON parsing. Returns false on any error (docker missing, network,
// image absent).
func HasArm64Image(image string) bool {
	_, arm64, _ := checkImageArch(image)
	return arm64
}

// checkImageArch probes a container image's manifest list and reports which
// architectures are present. hasAmd64 covers amd64/x86_64; hasArm64 covers
// arm64/aarch64. queryErr is true when docker is absent or the call fails.
func checkImageArch(image string) (hasAmd64, hasArm64, queryErr bool) {
	if !shell.CommandExists("docker") {
		return false, false, true
	}
	out, _, err := shell.Capture("docker", "manifest", "inspect", image)
	if err != nil || out == "" {
		return false, false, true
	}
	type entry struct {
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	}
	var m struct {
		Manifests    []entry `json:"manifests"`
		ManifestsAlt []entry `json:"Manifests"`
	}
	_ = json.Unmarshal([]byte(out), &m)
	for _, x := range append(m.Manifests, m.ManifestsAlt...) {
		switch x.Platform.Architecture {
		case "amd64", "x86_64":
			hasAmd64 = true
		case "arm64", "aarch64":
			hasArm64 = true
		}
	}
	return hasAmd64, hasArm64, false
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

// OpenTofu ensures the `tofu` binary matches cfg.OpenTofuVersion,
// downloading the OpenTofu release zip and extracting `tofu` into
// /usr/local/bin when missing or out of date.
//
// `tofu version -json` emits the same schema Terraform did, including the
// `terraform_version` key — we reuse the existing parser for that reason.
func OpenTofu(cfg *config.Config) error {
	if err := openTofuInner(cfg); err != nil {
		logx.Die("Failed to install OpenTofu (check OPENTOFU_VERSION=%s and network): %v", cfg.OpenTofuVersion, err)
	}
	return nil
}

func openTofuInner(cfg *config.Config) error {
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
		return fmt.Errorf("download OpenTofu from %s: %w", url, err)
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

// firstVersionOn runs `name args...` and extracts the first
// "vX.Y.Z"-looking token from stdout+stderr.
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

// installSystemPackage installs a single OS package, branching by the
// available package manager (apt-get / dnf / yum / apk).
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

// ─── deps check / upgrade ─────────────────────────────────────────────────────

// DepCheck records the current state of one CLI dependency.
type DepCheck struct {
	Name string
	Want string // pinned version or "any"
	Have string // detected version or "not found"
	OK   bool
	Skip bool // built-in (no check needed)
}

// ImageCheck records architecture availability for one container image.
type ImageCheck struct {
	Name  string // human-friendly label
	Image string // full image:tag reference
	Amd64 bool   // has amd64/x86-64 in manifest list
	Arm64 bool   // has arm64 in manifest list
	Err   bool   // docker absent or network failure
}

// CheckDeps returns the installed-vs-required status for every CLI tool yage
// needs, without installing anything. Safe to call from the TUI.
func CheckDeps(cfg *config.Config) []DepCheck {
	check := func(name, want, have string) DepCheck {
		ok := versionx.Match(have, want)
		if have == "" {
			have = "not found"
		}
		return DepCheck{Name: name, Want: want, Have: have, OK: ok}
	}
	var out []DepCheck
	out = append(out, DepCheck{Name: "kind", Want: "embedded", Have: "embedded", OK: true, Skip: true})
	out = append(out, check("kubectl", cfg.KubectlVersion, kubectlClientGitVersion()))
	out = append(out, check("clusterctl", cfg.ClusterctlVersion, clusterctlGitVersion()))
	out = append(out, check("tofu", cfg.OpenTofuVersion, tofuJSONVersion()))
	if cfg.CiliumCLIVersion != "" {
		out = append(out, check("cilium", cfg.CiliumCLIVersion, firstVersionOn("cilium", "version", "2>&1")))
	}
	if cfg.ArgoCD.Enabled {
		out = append(out, check("argocd", cfg.ArgoCD.Version, firstVersionOn("argocd", "version", "--client", "2>&1")))
	}
	if cfg.KyvernoEnabled {
		out = append(out, check("kyverno", cfg.KyvernoCLIVersion, firstVersionOn("kyverno", "version", "2>&1")))
	}
	if cfg.CertManagerEnabled {
		out = append(out, check("cmctl", cfg.CmctlVersion, firstVersionOn("cmctl", "version", "2>&1")))
	}
	helmHave := firstVersionOn("helm", "version")
	out = append(out, DepCheck{Name: "helm", Want: "any", Have: orHelmMissing(helmHave), OK: helmHave != ""})
	dockerHave := firstVersionOn("docker", "--version")
	out = append(out, DepCheck{Name: "docker", Want: "any", Have: orHelmMissing(dockerHave), OK: dockerHave != ""})
	return out
}

func orHelmMissing(v string) string {
	if v == "" {
		return "not found"
	}
	return v
}

// CheckProviderImages checks whether the container images required by the
// active provider have arm64 support in their registry manifest lists.
// Returns an empty slice when docker is absent. Safe to call from the TUI.
func CheckProviderImages(cfg *config.Config) []ImageCheck {
	if !shell.CommandExists("docker") {
		return nil
	}
	var images []struct{ name, ref string }
	if cfg.CAPICoreImage != "" {
		images = append(images, struct{ name, ref string }{"CAPI core", cfg.CAPICoreImage})
	}
	if cfg.CAPIBootstrapImage != "" {
		images = append(images, struct{ name, ref string }{"CAPI bootstrap", cfg.CAPIBootstrapImage})
	}
	if cfg.CAPIControlplaneImage != "" {
		images = append(images, struct{ name, ref string }{"CAPI controlplane", cfg.CAPIControlplaneImage})
	}
	if cfg.IPAMImage != "" {
		images = append(images, struct{ name, ref string }{"IPAM", cfg.IPAMImage})
	}
	if cfg.CAPMOXImageRepo != "" {
		tag := cfg.CAPMOXVersion
		if tag == "" {
			tag = "latest"
		}
		images = append(images, struct{ name, ref string }{"CAPMOX", cfg.CAPMOXImageRepo + ":" + tag})
	}
	var out []ImageCheck
	for _, img := range images {
		amd64, arm64, queryErr := checkImageArch(img.ref)
		out = append(out, ImageCheck{Name: img.name, Image: img.ref, Amd64: amd64, Arm64: arm64, Err: queryErr})
	}
	return out
}

// UpgradeDeps installs or upgrades every CLI tool to its pinned version.
// Unlike the individual exported functions, this returns the first error
// rather than calling logx.Die, making it safe to call from the TUI.
func UpgradeDeps(cfg *config.Config) error {
	if err := Kubectl(cfg); err != nil {
		return fmt.Errorf("kubectl: %w", err)
	}
	if err := Clusterctl(cfg); err != nil {
		return fmt.Errorf("clusterctl: %w", err)
	}
	if err := openTofuInner(cfg); err != nil {
		return fmt.Errorf("tofu: %w", err)
	}
	if cfg.CiliumCLIVersion != "" {
		if err := ciliumCLI(cfg); err != nil {
			return fmt.Errorf("cilium: %w", err)
		}
	}
	if cfg.ArgoCD.Enabled {
		if err := argoCDCLI(cfg); err != nil {
			return fmt.Errorf("argocd: %w", err)
		}
	}
	if cfg.KyvernoEnabled {
		if err := kyvernoCLI(cfg); err != nil {
			return fmt.Errorf("kyverno: %w", err)
		}
	}
	if cfg.CertManagerEnabled {
		if err := cmctlInner(cfg); err != nil {
			return fmt.Errorf("cmctl: %w", err)
		}
	}
	if err := Helm(); err != nil {
		return fmt.Errorf("helm: %w", err)
	}
	return nil
}