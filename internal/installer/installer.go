// Package installer ports install_binary and every ensure_* helper from
// bootstrap-capi.sh (lines ~1994-2858). These functions install or upgrade
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
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
	"github.com/lpasquali/bootstrap-capi/internal/sysinfo"
	"github.com/lpasquali/bootstrap-capi/internal/versionx"
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

// Kind, Kubectl, CiliumCLI, ArgoCDCLI, KyvernoCLI, and Cmctl installers were
// removed: every operation that used to shell out to those CLIs now goes
// through k8s.io/client-go (kubectl), sigs.k8s.io/kind/pkg/cluster (kind),
// or the equivalent vendor library. End users wanting interactive CLIs to
// poke at the deployed system install them themselves; this binary no longer
// pins or ships them.

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

// Helm and EnsureHelmPresent were removed: an audit showed the rest of the
// codebase has zero `helm` shell-outs, so installing the helm CLI is no longer
// necessary. If a future caller needs helm functionality it should use
// `helm.sh/helm/v3` in-process rather than shelling out to the binary.

// Docker installs Docker Engine when it is not already on PATH. Ports
// bash L8020-L8034 (curl get.docker.com, optional usermod, systemctl
// enable --now docker).
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

// UpgradeDocker upgrades docker-ce / docker-ce-cli / containerd.io via
// the host package manager. Ports bash L8098-L8109. Best-effort: unknown
// package managers are warned about and skipped.
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
