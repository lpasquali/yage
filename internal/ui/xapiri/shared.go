// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// shared.go — helpers shared across the dashboard and supporting
// code. Includes workload shape sync, fork detection, credential
// checks, and app-bucket parsing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// detectFork is the auto-detection rule from §22.1. Pure function
// (no I/O) so it stays trivially testable.
//
// Priority: (1) cfg.InfraProvider — the authoritative saved value.
// (2) airgapped flag. (3) env-var heuristics for fresh sessions.
func detectFork(cfg *config.Config) forkType {
	if cfg.InfraProvider != "" {
		if provider.AirgapCompatible(cfg.InfraProvider) {
			return forkOnPrem
		}
		return forkCloud
	}
	if cfg.Airgapped {
		return forkOnPrem
	}
	cloud := os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
		os.Getenv("AZURE_SUBSCRIPTION_ID") != "" ||
		os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != ""
	if cloud {
		return forkCloud
	}
	if os.Getenv("PROXMOX_URL") != "" {
		return forkOnPrem
	}
	return forkUnknown
}

// syncWorkloadShapeToCfg copies xapiri's workload answers onto
// cfg.Workload so feasibility.Check and any cost estimator that keys
// off the stated product shape see the same numbers the user typed.
func syncWorkloadShapeToCfg(cfg *config.Config, w workloadShape, resil resilienceTier, env envTier, fork forkType) {
	if cfg == nil {
		return
	}
	apps := make([]config.AppGroup, 0, len(w.Apps))
	for _, b := range w.Apps {
		if b.Count <= 0 {
			continue
		}
		tpl := strings.ToLower(strings.TrimSpace(b.Template))
		if tpl != "light" && tpl != "medium" && tpl != "heavy" {
			continue
		}
		apps = append(apps, config.AppGroup{Count: b.Count, Template: tpl})
	}
	var res string
	switch resil {
	case resilienceHA:
		res = "ha"
	case resilienceHAMulti:
		res = "ha-mr"
	default:
		res = "single"
	}
	var envStr string
	switch env {
	case envStaging:
		envStr = "staging"
	case envProd:
		envStr = "prod"
	default:
		envStr = "dev"
	}
	egress := w.EgressGBMo
	if fork == forkOnPrem {
		// On-prem fork never prompts egress; feasibility's §23.6
		// sandbag only applies to the cloud cost-compare path — use
		// the same lazy default as the cloud prompt so Check() doesn't
		// attach a spurious "egress unset" block when a later CLI run
		// sets BudgetUSDMonth on the same cfg.
		if egress <= 0 && w.DBGB > 0 {
			egress = w.DBGB * 2
		}
	}
	cfg.Workload = config.WorkloadShape{
		Apps:          apps,
		DatabaseGB:    w.DBGB,
		EgressGBMonth: egress,
		Resilience:    res,
		Environment:   envStr,
		HasQueue:      w.HasQueue,
		HasObjStore:   w.HasObjStore,
		HasCache:      w.HasCache,
	}
	// Stamp add-on resource overrides so cost.AddonCostItem reads them.
	// Only write non-zero values; a disabled add-on keeps any prior override
	// so re-enabling it on the next run restores the operator's sizing.
	if w.HasQueue {
		if w.QueueCPUMilli > 0 {
			cfg.MQCPUMillicoresOverride = w.QueueCPUMilli
		}
		if w.QueueMemMiB > 0 {
			cfg.MQMemoryMiBOverride = w.QueueMemMiB
		}
		if w.QueueVolGB > 0 {
			cfg.MQVolumeGBOverride = w.QueueVolGB
		}
	}
	if w.HasObjStore {
		if w.ObjStoreCPUMilli > 0 {
			cfg.ObjStoreCPUMillicoresOverride = w.ObjStoreCPUMilli
		}
		if w.ObjStoreMemMiB > 0 {
			cfg.ObjStoreMemoryMiBOverride = w.ObjStoreMemMiB
		}
		if w.ObjStoreVolGB > 0 {
			cfg.ObjStoreVolumeGBOverride = w.ObjStoreVolGB
		}
	}
	if w.HasCache {
		if w.CacheCPUMilli > 0 {
			cfg.CacheCPUMillicoresOverride = w.CacheCPUMilli
		}
		if w.CacheMemMiB > 0 {
			cfg.CacheMemoryMiBOverride = w.CacheMemMiB
		}
	}
}

// formatAppBuckets renders the apps in the same shape parseAppBuckets
// reads. Used to populate the dashboard's bracketed defaults.
func formatAppBuckets(b []appBucket) string {
	if len(b) == 0 {
		return ""
	}
	parts := make([]string, 0, len(b))
	for _, x := range b {
		parts = append(parts, fmt.Sprintf("%d %s", x.Count, x.Template))
	}
	return strings.Join(parts, " ")
}

// parseAppBuckets is the lenient parser. Accepts:
//
//	"6 medium 2 heavy"
//	"6 medium, 2 heavy"
//	"6×medium 2×heavy"
//	"6xmedium,2xheavy"
//
// Anything not in {light,medium,heavy} after a count is dropped on
// the floor (the dashboard re-validates if the result is empty).
func parseAppBuckets(s string) []appBucket {
	clean := strings.NewReplacer(",", " ", "×", " ", "x", " ", "*", " ").Replace(s)
	tokens := strings.Fields(clean)
	out := []appBucket{}
	for i := 0; i+1 < len(tokens); i += 2 {
		n, err := strconv.Atoi(tokens[i])
		if err != nil || n < 0 {
			continue
		}
		tpl := strings.ToLower(tokens[i+1])
		if tpl != "light" && tpl != "medium" && tpl != "heavy" {
			continue
		}
		out = append(out, appBucket{Count: n, Template: tpl})
	}
	return out
}

// awsAnyCredentialsAvailable returns true when AWS credentials are available
// in any form: explicit key/secret in cfg, AWS SDK env vars (AWS_ACCESS_KEY_ID,
// AWS_PROFILE), or the standard ~/.aws/credentials / ~/.aws/config files.
// newAWSPricingClient now falls back to the SDK default chain, so any of these
// sources will allow a successful Pricing API call.
func awsAnyCredentialsAvailable(cfg *config.Config) bool {
	if cfg.Cost.Credentials.AWSAccessKeyID != "" && cfg.Cost.Credentials.AWSSecretAccessKey != "" {
		return true
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range []string{".aws/credentials", ".aws/config"} {
			if _, e := os.Stat(filepath.Join(home, rel)); e == nil {
				return true
			}
		}
	}
	return false
}

// gcpAnyCredentialsAvailable mirrors awsAnyCredentialsAvailable for GCP: checks
// the explicit cfg credential first, then the same env-var fallbacks that the
// pricing fetcher uses (YAGE_GCP_API_KEY, GOOGLE_BILLING_API_KEY).
func gcpAnyCredentialsAvailable(cfg *config.Config) bool {
	if cfg.Cost.Credentials.GCPAPIKey != "" {
		return true
	}
	return os.Getenv("YAGE_GCP_API_KEY") != "" || os.Getenv("GOOGLE_BILLING_API_KEY") != ""
}

// disableProvidersMissingCredentials syncs cfg.SkipProviders with credential
// availability. Azure, Linode, and OCI use public APIs and are never touched.
// AWS is skipped only when no credentials exist in any form (explicit key/secret,
// env vars, or ~/.aws/ files) — it falls back to the SDK credential chain.
// When cfg.InfraProvider is set explicitly (non-defaulted), only that provider
// is checked; others are left alone.
//
// Critically, providers are also REMOVED from SkipProviders when credentials
// become available mid-session (e.g. after the [costs] credential form is
// submitted), so the live cost bar updates without requiring a restart.
func disableProvidersMissingCredentials(cfg *config.Config) {
	type check struct {
		name    string
		missing bool
	}
	checks := []check{
		{"aws", !awsAnyCredentialsAvailable(cfg)},
		{"gcp", !gcpAnyCredentialsAvailable(cfg)},
		{"hetzner", cfg.Cost.Credentials.HetznerToken == ""},
		{"digitalocean", cfg.Cost.Credentials.DigitalOceanToken == ""},
		{"ibmcloud", cfg.Cost.Credentials.IBMCloudAPIKey == ""},
	}
	skipped := make(map[string]struct{})
	for _, p := range strings.Split(cfg.SkipProviders, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			skipped[p] = struct{}{}
		}
	}
	for _, c := range checks {
		if cfg.InfraProvider != "" && !cfg.InfraProviderDefaulted && c.name != cfg.InfraProvider {
			continue
		}
		if c.missing {
			skipped[c.name] = struct{}{}
		} else {
			delete(skipped, c.name) // credentials now available — restore provider
		}
	}
	names := make([]string, 0, len(skipped))
	for n := range skipped {
		names = append(names, n)
	}
	cfg.SkipProviders = strings.Join(names, ",")
}
