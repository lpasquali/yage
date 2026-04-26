// Package xapiri implements yage's interactive configuration TUI,
// invoked via `yage --xapiri`.
//
// xapiri are sacred spirits in the Yanomami people's cosmology.
// yage runs xapiri to get help from the spirits to create a
// visionary deployment — an interactive walkthrough that surfaces
// every config knob, validates choices against the active provider,
// and persists the result to a Secret in the local kind cluster
// (yage-system namespace) before any state is changed on the target
// cloud. yage stores its config and provider credentials in kind
// Secrets; local disk is used only for encrypted kind cluster
// backup/restore archives.
//
// The walkthrough is a quiet, line-oriented stroll through the
// primary config knobs: provider, cluster identity, sizing, the
// active provider's own fields (surfaced via reflection so new
// providers light up automatically), cost-API credentials when
// online, and the optional add-on toggles. The resolved config is
// echoed back with sensitive fields masked, then — on confirmation —
// written to the kind cluster's bootstrap-config Secret. When that
// Secret writer isn't reachable yet (e.g. during early bootstrap or
// before Track B's kindsync helper lands), the answers are saved to
// ~/.config/yage/bootstrap.yaml; the next non-xapiri run picks them
// up on first sync.
package xapiri

import (
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// Run starts the interactive configuration walkthrough. Returns the
// exit code main should propagate: 0 on success (whether persisted to
// kind or fallback) or when the user declines to write at the review
// step; non-zero only on hard I/O failures.
func Run(w io.Writer, cfg *config.Config) int {
	if cfg == nil {
		fmt.Fprintln(w, "xapiri: nil config (internal error)")
		return 2
	}
	r := newReader(os.Stdin, w)

	r.info("xapiri — let's shape this deployment together.")
	r.info("press enter to keep the value in [brackets].")

	chooseProvider(r, cfg)
	chooseClusterIdentity(r, cfg)
	chooseClusterSizing(r, cfg)
	promptProviderFields(r, cfg, cfg.InfraProvider)
	if !cfg.Airgapped {
		chooseCostCredentials(r, cfg)
	}
	chooseAddOns(r, cfg)

	if !reviewAndConfirm(r, cfg) {
		r.info("nothing written. the spirits rest.")
		return 0
	}

	dest, err := persistConfig(w, cfg)
	if err != nil {
		fmt.Fprintf(w, "xapiri: failed to persist config: %v\n", err)
		return 1
	}
	r.info("written to %s", dest)
	return 0
}

// chooseProvider lets the user pick from the registered providers,
// filtered through the airgap allowlist when cfg.Airgapped is true.
// The current cfg.InfraProvider is surfaced as the default so users
// who already have INFRA_PROVIDER set in their env get a one-keypress
// confirmation.
func chooseProvider(r *reader, cfg *config.Config) {
	r.section("provider")
	names := provider.AirgapFilter(provider.Registered(), cfg.Airgapped)
	if len(names) == 0 {
		r.info("no providers registered — this build is empty.")
		return
	}
	cur := cfg.InfraProvider
	if cur == "" {
		cur = names[0]
	}
	cfg.InfraProvider = r.promptChoice("which infrastructure provider?", names, cur)
	// Once the user picks, treat it as explicit for downstream warnings.
	cfg.InfraProviderDefaulted = false
}

// chooseClusterIdentity prompts for the workload cluster's name,
// namespace, and Kubernetes version. Cluster name + namespace are
// validated as DNS-1123 labels; the version is free-form because
// CAPI accepts a few alternative spellings (with and without leading
// "v") and we don't want to be more strict than the upstream.
func chooseClusterIdentity(r *reader, cfg *config.Config) {
	r.section("cluster identity")
	cfg.WorkloadClusterName = r.promptDNSLabel("workload cluster name", cfg.WorkloadClusterName)
	cfg.WorkloadClusterNamespace = r.promptDNSLabel("workload cluster namespace", cfg.WorkloadClusterNamespace)
	cfg.WorkloadKubernetesVersion = r.promptString("workload kubernetes version", cfg.WorkloadKubernetesVersion)
	// Mark them explicit so any downstream "did the user really mean
	// the default?" warnings stay quiet.
	cfg.WorkloadClusterNameExplicit = true
	cfg.WorkloadClusterNamespaceExplicit = true
}

// chooseClusterSizing prompts for the control-plane and worker node
// counts. Both are stored as strings because CAPI templates substitute
// them as ${VAR} tokens — keeping them as strings avoids a parse step
// at template-render time.
func chooseClusterSizing(r *reader, cfg *config.Config) {
	r.section("cluster sizing")
	cfg.ControlPlaneMachineCount = r.promptInt("control-plane machines", cfg.ControlPlaneMachineCount)
	cfg.WorkerMachineCount = r.promptInt("worker machines", cfg.WorkerMachineCount)
}

// chooseCostCredentials surfaces the four cost-API credentials. We
// only call this when !cfg.Airgapped — there's no point asking for
// pricing tokens when the binary won't reach the internet anyway.
func chooseCostCredentials(r *reader, cfg *config.Config) {
	r.section("cost-API credentials")
	r.info("(optional — skip any you don't have; cost compare just won't show that vendor)")
	c := &cfg.Cost.Credentials
	c.GCPAPIKey = r.promptSecret("GCP billing API key", c.GCPAPIKey)
	c.HetznerToken = r.promptSecret("Hetzner Cloud token", c.HetznerToken)
	c.DigitalOceanToken = r.promptSecret("DigitalOcean token", c.DigitalOceanToken)
	c.IBMCloudAPIKey = r.promptSecret("IBM Cloud API key", c.IBMCloudAPIKey)
}

// chooseAddOns walks the small set of optional, top-level cluster
// add-ons that flip with a single bool. The list is intentionally
// short — the goal is to nudge the operator through the choices that
// most affect deployment shape, not to exhaustively reproduce every
// flag in `yage --help`.
func chooseAddOns(r *reader, cfg *config.Config) {
	r.section("optional add-ons")
	cfg.ArgoCDEnabled = r.promptYesNo("install Argo CD on the management cluster?", cfg.ArgoCDEnabled)
	cfg.WorkloadArgoCDEnabled = r.promptYesNo("install Argo CD on the workload cluster?", cfg.WorkloadArgoCDEnabled)
	cfg.EnableMetricsServer = r.promptYesNo("install metrics-server on management?", cfg.EnableMetricsServer)
	cfg.EnableWorkloadMetricsServer = r.promptYesNo("install metrics-server on workload?", cfg.EnableWorkloadMetricsServer)
	cfg.CertManagerEnabled = r.promptYesNo("install cert-manager?", cfg.CertManagerEnabled)
	cfg.KyvernoEnabled = r.promptYesNo("install Kyverno?", cfg.KyvernoEnabled)
}

// reviewAndConfirm echoes the resolved config back to the user with
// sensitive fields masked, then asks for a final write-or-bail
// confirmation.
func reviewAndConfirm(r *reader, cfg *config.Config) bool {
	r.section("review")
	r.info("provider:               %s", cfg.InfraProvider)
	r.info("workload cluster:       %s/%s @ %s",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, cfg.WorkloadKubernetesVersion)
	r.info("sizing:                 control-plane=%s worker=%s",
		cfg.ControlPlaneMachineCount, cfg.WorkerMachineCount)
	r.info("airgapped:              %v", cfg.Airgapped)
	if !cfg.Airgapped {
		c := cfg.Cost.Credentials
		r.info("cost.gcp_api_key:       %s", maskValue(c.GCPAPIKey))
		r.info("cost.hetzner_token:     %s", maskValue(c.HetznerToken))
		r.info("cost.digitalocean_tok:  %s", maskValue(c.DigitalOceanToken))
		r.info("cost.ibmcloud_api_key:  %s", maskValue(c.IBMCloudAPIKey))
	}
	r.info("argocd (mgmt/workload): %v / %v", cfg.ArgoCDEnabled, cfg.WorkloadArgoCDEnabled)
	r.info("metrics-server:         mgmt=%v workload=%v",
		cfg.EnableMetricsServer, cfg.EnableWorkloadMetricsServer)
	r.info("cert-manager / kyverno: %v / %v", cfg.CertManagerEnabled, cfg.KyvernoEnabled)
	reviewProviderFields(r, cfg, cfg.InfraProvider)
	return r.promptYesNo("write to kind?", true)
}

// reviewProviderFields prints every top-level string field on the
// active provider's sub-struct, mirroring the prompt order in
// promptProviderFields so the review reads like a play-back of what
// the user just typed.
func reviewProviderFields(r *reader, cfg *config.Config, name string) {
	sub, ok := providerSubStruct(cfg, name)
	if !ok {
		return
	}
	r.info("%s settings:", name)
	t := sub.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		v := sub.Field(i).String()
		if v == "" {
			continue
		}
		// Tokens / secrets / passphrases / passwords get masked. We
		// match on the field name rather than a tag because the
		// existing config types don't carry a `sensitive:"true"` tag
		// and adding one would touch internal/config — out of track.
		if isSensitiveFieldName(f.Name) {
			v = maskValue(v)
		}
		r.info("  %s: %s", f.Name, v)
	}
}

// promptProviderFields walks every top-level string field on the
// active provider's sub-struct and offers a prompt for each. New
// providers (and new fields on existing ones) light up automatically:
// when Track E adds a field, this loop surfaces it without code
// changes here.
//
// Nested structs (e.g. ProxmoxConfig.Mgmt) are skipped on this pass
// — the Type.Kind() filter to reflect.String stops at the first
// level. A future pass can recurse into Mgmt sub-structs once the
// rest of the walkthrough is settled.
func promptProviderFields(r *reader, cfg *config.Config, name string) {
	sub, ok := providerSubStruct(cfg, name)
	if !ok {
		return
	}
	r.section(fmt.Sprintf("%s settings", name))
	t := sub.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		// We deliberately don't surface every single Proxmox field
		// (there are ~50) — the user just wants the knobs that
		// matter at bootstrap. Skip internal bookkeeping fields whose
		// names start with the "Bootstrap" prefix; they're written by
		// kindsync and have no meaningful default to set by hand.
		if isInternalBookkeeping(f.Name) {
			continue
		}
		cur := sub.Field(i).String()
		var ans string
		if isSensitiveFieldName(f.Name) {
			ans = r.promptSecret(f.Name, cur)
		} else {
			ans = r.promptString(f.Name, cur)
		}
		if sub.Field(i).CanSet() {
			sub.Field(i).SetString(ans)
		}
	}
}

// providerSubStruct resolves cfg.Providers.<ProperCase(name)> via
// reflection. Returns the Value + a bool reporting whether the field
// was found — providers registered by name but missing a sub-struct
// (the "minstub" path used in tests) silently skip the section.
func providerSubStruct(cfg *config.Config, name string) (reflect.Value, bool) {
	pv := reflect.ValueOf(&cfg.Providers).Elem()
	sub := pv.FieldByName(properCase(name))
	if !sub.IsValid() || sub.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return sub, true
}

// properCase maps a provider's registry id ("aws", "ibmcloud",
// "digitalocean", "proxmox", …) to the matching field name on the
// Providers struct. The mapping is small enough that a switch is
// clearer (and faster) than a generic title-case helper, and the
// switch surfaces "we don't know about this provider yet" loudly
// when a new one is registered.
func properCase(name string) string {
	switch name {
	case "aws":
		return "AWS"
	case "azure":
		return "Azure"
	case "gcp":
		return "GCP"
	case "hetzner":
		return "Hetzner"
	case "digitalocean":
		return "DigitalOcean"
	case "linode":
		return "Linode"
	case "oci":
		return "OCI"
	case "ibmcloud":
		return "IBMCloud"
	case "proxmox":
		return "Proxmox"
	default:
		// Last-ditch: capitalize the first letter. Better to surface a
		// blank section than to silently skip a registered provider
		// just because the mapping is missing.
		if name == "" {
			return ""
		}
		return string(name[0]-32) + name[1:]
	}
}

// isSensitiveFieldName recognises field names that should be masked
// in the review pass and prompted via promptSecret. We match by
// suffix on common conventions; missing a field here only loses the
// echo-mask, never functionality.
func isSensitiveFieldName(name string) bool {
	for _, suf := range []string{"Token", "Secret", "Password", "APIKey", "Passphrase"} {
		if hasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// isInternalBookkeeping spots provider-config fields that aren't
// meant to be hand-tuned during the walkthrough — e.g. cached
// kindsync-side flags and bootstrap-Secret name placeholders. They'd
// just clutter the prompt list with bookkeeping the user shouldn't
// be touching.
func isInternalBookkeeping(name string) bool {
	for _, pre := range []string{"Bootstrap", "KindCAPMOX", "Identity"} {
		if hasPrefix(name, pre) {
			return true
		}
	}
	return false
}

// hasPrefix / hasSuffix — tiny helpers so we don't pull in the strings
// package twice for two trivial calls. Equivalent to strings.HasPrefix
// / strings.HasSuffix but kept inline so the file is self-describing.
func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func hasSuffix(s, p string) bool { return len(s) >= len(p) && s[len(s)-len(p):] == p }
