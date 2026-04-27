// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	proxmoxsdk "github.com/luthermonson/go-proxmox"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// newSDKClient constructs a go-proxmox SDK client for baseURL (must
// include the /api2/json suffix). tokenID and secret are used as the
// PVEAPIToken credential. When insecure is true, TLS verification is
// skipped.
func newSDKClient(baseURL, tokenID, secret string, insecure bool) *proxmoxsdk.Client {
	opts := []proxmoxsdk.Option{
		proxmoxsdk.WithAPIToken(tokenID, secret),
	}
	if insecure {
		tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user-controlled flag
		opts = append(opts, proxmoxsdk.WithHTTPClient(&http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}))
	}
	return proxmoxsdk.NewClient(baseURL, opts...)
}

// ResolveRegionAndNodeFromPVEAuth fills empty
// cfg.Providers.Proxmox.Node and cfg.Providers.Proxmox.Region by
// querying the Proxmox cluster status endpoint.
//
// authValue is the full PVEAPIToken header value ("user!token=secret"
// or "PVEAPIToken=user!token=secret"). Silent no-op when already
// populated, URL is empty, or auth is empty.
func ResolveRegionAndNodeFromPVEAuth(cfg *config.Config, authValue string) error {
	if cfg.Providers.Proxmox.URL == "" || authValue == "" {
		return nil
	}
	if cfg.Providers.Proxmox.Region != "" && cfg.Providers.Proxmox.Node != "" {
		return nil
	}

	// Strip the "PVEAPIToken=" prefix to get "tokenID=secret".
	raw := authValue
	if strings.HasPrefix(strings.ToLower(raw), "pveapitoken=") {
		raw = raw[len("PVEAPIToken="):]
	}

	// Split "tokenID=secret" — last '=' separates tokenID from secret.
	tokenID, secret := splitTokenAuth(raw)

	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)
	c := newSDKClient(APIJSONURL(cfg), tokenID, secret, insecure)
	ctx := context.Background()

	// c.Cluster() calls /cluster/status and populates Cluster.Name +
	// Cluster.Nodes (each NodeStatus carries Local and Name fields).
	cluster, err := c.Cluster(ctx)
	if err != nil {
		// Non-fatal: fall through with whatever was resolved.
		return nil
	}

	if cfg.Providers.Proxmox.Node == "" && cluster != nil {
		// Prefer the node marked as local; fall back to lexically first.
		var locals, all []string
		for _, ns := range cluster.Nodes {
			n := ns.Name
			if n == "" {
				continue
			}
			all = append(all, n)
			if ns.Local == 1 {
				locals = append(locals, n)
			}
		}
		switch {
		case len(locals) > 0:
			cfg.Providers.Proxmox.Node = locals[0]
		case len(all) > 0:
			cfg.Providers.Proxmox.Node = minString(all)
		}
	}

	if cfg.Providers.Proxmox.Region == "" && cluster != nil && cluster.Name != "" {
		cfg.Providers.Proxmox.Region = cluster.Name
	}
	// Final fallback: use the resolved node name as region.
	if cfg.Providers.Proxmox.Region == "" && cfg.Providers.Proxmox.Node != "" {
		cfg.Providers.Proxmox.Region = cfg.Providers.Proxmox.Node
	}

	if cfg.Providers.Proxmox.Region != "" {
		logx.Log("Derived PROXMOX_REGION from Proxmox API: %s", cfg.Providers.Proxmox.Region)
	}
	if cfg.Providers.Proxmox.Node != "" {
		logx.Log("Derived PROXMOX_NODE from Proxmox API: %s", cfg.Providers.Proxmox.Node)
	}
	return nil
}

// ResolveRegionAndNodeFromAdminAPI resolves region and node using
// the admin token.
func ResolveRegionAndNodeFromAdminAPI(cfg *config.Config) error {
	if cfg.Providers.Proxmox.AdminUsername == "" || cfg.Providers.Proxmox.AdminToken == "" {
		return nil
	}
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.Providers.Proxmox.AdminUsername, cfg.Providers.Proxmox.AdminToken)
	return ResolveRegionAndNodeFromPVEAuth(cfg, auth)
}

// ResolveRegionAndNodeFromClusterctlAPI resolves region and node
// using PROXMOX_TOKEN + PROXMOX_SECRET (the CAPI token), normalising
// the secret first.
func ResolveRegionAndNodeFromClusterctlAPI(cfg *config.Config) error {
	if cfg.Providers.Proxmox.CAPIToken == "" || cfg.Providers.Proxmox.CAPISecret == "" {
		return nil
	}
	sec := NormalizeTokenSecret(cfg.Providers.Proxmox.CAPISecret, cfg.Providers.Proxmox.CAPIToken)
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.Providers.Proxmox.CAPIToken, sec)
	return ResolveRegionAndNodeFromPVEAuth(cfg, auth)
}

// CheckAdminAPIConnectivity validates:
//  1. GET /api2/json/version returns 200 (credentials valid)
//  2. GET /api2/json/access/roles returns 200 (admin has required
//     privileges for OpenTofu role bootstrap)
//
// Dies on any failure.
func CheckAdminAPIConnectivity(cfg *config.Config) {
	if cfg.Providers.Proxmox.URL == "" {
		logx.Die("PROXMOX_URL is required for OpenTofu identity orchestrator.")
	}
	if cfg.Providers.Proxmox.AdminUsername == "" {
		logx.Die("PROXMOX_ADMIN_USERNAME is required for OpenTofu identity orchestrator.")
	}
	if cfg.Providers.Proxmox.AdminToken == "" {
		logx.Die("PROXMOX_ADMIN_TOKEN is required for OpenTofu identity orchestrator.")
	}

	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)
	c := newSDKClient(APIJSONURL(cfg),
		cfg.Providers.Proxmox.AdminUsername,
		cfg.Providers.Proxmox.AdminToken,
		insecure)
	ctx := context.Background()

	logx.Log("Validating Proxmox admin API credentials at %s...", HostBaseURL(cfg))

	if _, err := c.Version(ctx); err != nil {
		if proxmoxsdk.IsNotAuthorized(err) {
			logx.Die("Proxmox admin API token unauthorized (401). Check PROXMOX_ADMIN_USERNAME token ID and PROXMOX_ADMIN_TOKEN secret.")
		}
		logx.Die("Could not reach Proxmox API at %s. Check PROXMOX_URL and network connectivity.", HostBaseURL(cfg))
	}
	logx.Log("Proxmox admin API token validated (HTTP 200 on /version).")

	if _, err := c.Roles(ctx); err != nil {
		if proxmoxsdk.IsNotAuthorized(err) {
			logx.Die("Proxmox admin token cannot access /access/roles (401). Token lacks required privileges for OpenTofu role creation.")
		}
		logx.Die("Unexpected error while checking /access/roles permissions for OpenTofu orchestrator.")
	}
	logx.Log("Proxmox admin token can access /access/roles (required for role bootstrap).")
}

// ResolveAvailableClusterSetIDForRoles only applies to numeric
// CLUSTER_SET_IDs — UUIDs are unique enough and skip this search.
// Walks PVE /access/roles, /access/users, and per-user
// /access/users/<id>/token to find the first integer starting from
// cfg.ClusterSetID that has no collision for the derived
// role/user/token names. On conflict, updates cfg.ClusterSetID +
// identity suffix + derived user/token IDs.
func ResolveAvailableClusterSetIDForRoles(cfg *config.Config) error {
	if !numericRE.MatchString(cfg.ClusterSetID) {
		return nil
	}

	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)
	c := newSDKClient(APIJSONURL(cfg),
		cfg.Providers.Proxmox.AdminUsername,
		cfg.Providers.Proxmox.AdminToken,
		insecure)
	ctx := context.Background()

	roles, err := c.Roles(ctx)
	if err != nil {
		logx.Warn("Failed to compute an available CLUSTER_SET_ID from Proxmox identity inventory; using explicit CLUSTER_SET_ID=%s.", cfg.ClusterSetID)
		return err
	}
	users, err := c.Users(ctx)
	if err != nil {
		logx.Warn("Failed to compute an available CLUSTER_SET_ID from Proxmox identity inventory; using explicit CLUSTER_SET_ID=%s.", cfg.ClusterSetID)
		return err
	}

	roleSet := map[string]bool{}
	for _, r := range roles {
		roleSet[r.RoleID] = true
	}
	userSet := map[string]bool{}
	for _, u := range users {
		userSet[u.UserID] = true
	}

	// tokenNameExists checks whether the given tokenName exists for a
	// given userID. Uses the SDK's User.GetAPITokens method.
	tokenNameExists := func(userID, name string) bool {
		if !userSet[userID] {
			return false
		}
		user, err := c.User(ctx, userID)
		if err != nil {
			return false
		}
		tokens, err := user.GetAPITokens(ctx)
		if err != nil {
			return false
		}
		full := userID + "!" + name
		for _, t := range tokens {
			if t.TokenID == full {
				return true
			}
			// The SDK may return only the bare name portion.
			if i := strings.IndexByte(t.TokenID, '!'); i >= 0 && t.TokenID[i+1:] == name {
				return true
			}
		}
		return false
	}

	csiPrefix := cfg.Providers.Proxmox.CSITokenPrefix
	capiPrefix := cfg.Providers.Proxmox.CAPITokenPrefix
	explicitCSIUser := strings.TrimSpace(cfg.Providers.Proxmox.CSIUserID)
	explicitCAPIUser := strings.TrimSpace(cfg.Providers.Proxmox.CAPIUserID)

	var candidate int
	fmt.Sscanf(cfg.ClusterSetID, "%d", &candidate)
	for {
		csiRole := fmt.Sprintf("Kubernetes-CSI-%d", candidate)
		capiRole := fmt.Sprintf("Kubernetes-CAPI-%d", candidate)
		csiUser := explicitCSIUser
		if csiUser == "" {
			csiUser = UserIDWithSuffix(DefaultCSIUserBase, fmt.Sprint(candidate))
		}
		capiUser := explicitCAPIUser
		if capiUser == "" {
			capiUser = UserIDWithSuffix(DefaultCAPIUserBase, fmt.Sprint(candidate))
		}
		csiTokenName := fmt.Sprintf("%s-%d", csiPrefix, candidate)
		capiTokenName := fmt.Sprintf("%s-%d", capiPrefix, candidate)

		if roleSet[csiRole] || roleSet[capiRole] {
			candidate++
			continue
		}
		if explicitCSIUser == "" && userSet[csiUser] {
			candidate++
			continue
		}
		if explicitCAPIUser == "" && userSet[capiUser] {
			candidate++
			continue
		}
		if userSet[csiUser] && tokenNameExists(csiUser, csiTokenName) {
			candidate++
			continue
		}
		if userSet[capiUser] && tokenNameExists(capiUser, capiTokenName) {
			candidate++
			continue
		}

		resolved := fmt.Sprint(candidate)
		if resolved != cfg.ClusterSetID {
			oldSetID := cfg.ClusterSetID
			oldSuffix := DeriveIdentitySuffix(oldSetID)
			oldCSIUser := UserIDWithSuffix(DefaultCSIUserBase, oldSuffix)
			oldCAPIUser := UserIDWithSuffix(DefaultCAPIUserBase, oldSuffix)
			oldCSITokenID := TokenIDForSet(oldCSIUser, cfg.Providers.Proxmox.CSITokenPrefix, oldSuffix)
			oldCAPITokenID := TokenIDForSet(oldCAPIUser, cfg.Providers.Proxmox.CAPITokenPrefix, oldSuffix)

			logx.Warn("CLUSTER_SET_ID=%s is already in use in Proxmox identity resources; using CLUSTER_SET_ID=%s.", cfg.ClusterSetID, resolved)
			cfg.ClusterSetID = resolved
			cfg.Providers.Proxmox.IdentitySuffix = DeriveIdentitySuffix(cfg.ClusterSetID)

			if cfg.Providers.Proxmox.CSIUserID == "" || cfg.Providers.Proxmox.CSIUserID == oldCSIUser {
				cfg.Providers.Proxmox.CSIUserID = UserIDWithSuffix(DefaultCSIUserBase, cfg.Providers.Proxmox.IdentitySuffix)
			}
			if cfg.Providers.Proxmox.CAPIUserID == "" || cfg.Providers.Proxmox.CAPIUserID == oldCAPIUser {
				cfg.Providers.Proxmox.CAPIUserID = UserIDWithSuffix(DefaultCAPIUserBase, cfg.Providers.Proxmox.IdentitySuffix)
			}
			if cfg.Providers.Proxmox.CSITokenID == "" || cfg.Providers.Proxmox.CSITokenID == oldCSITokenID {
				cfg.Providers.Proxmox.CSITokenID = TokenID(cfg.Providers.Proxmox.CSIUserID, cfg.Providers.Proxmox.CSITokenPrefix, cfg.Providers.Proxmox.IdentitySuffix)
			}
			if cfg.Providers.Proxmox.CAPIToken == "" || cfg.Providers.Proxmox.CAPIToken == oldCAPITokenID {
				cfg.Providers.Proxmox.CAPIToken = TokenID(cfg.Providers.Proxmox.CAPIUserID, cfg.Providers.Proxmox.CAPITokenPrefix, cfg.Providers.Proxmox.IdentitySuffix)
			}
			RefreshDerivedIdentityTokenIDs(cfg)
			RefreshDerivedCiliumClusterID(cfg)
		}
		return nil
	}
}

// EnsurePool creates a Proxmox pool with the given name if it doesn't
// already exist. Idempotent: returns nil when the pool is already
// present. Uses the admin token (the clusterctl token usually doesn't
// have Pool.Allocate). Caller skips when name is empty.
//
// Equivalent to `pveum pool add <name>` against the Proxmox REST API
// (POST /api2/json/pools).
func EnsurePool(cfg *config.Config, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	if cfg.Providers.Proxmox.AdminUsername == "" || cfg.Providers.Proxmox.AdminToken == "" {
		return fmt.Errorf("EnsurePool %s: admin credentials missing (PROXMOX_ADMIN_USERNAME / PROXMOX_ADMIN_TOKEN)", name)
	}
	if cfg.Providers.Proxmox.URL == "" {
		return fmt.Errorf("EnsurePool %s: PROXMOX_URL is empty", name)
	}

	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)
	c := newSDKClient(APIJSONURL(cfg),
		cfg.Providers.Proxmox.AdminUsername,
		cfg.Providers.Proxmox.AdminToken,
		insecure)
	ctx := context.Background()

	// Idempotency probe: fetching the pool returns an error when it
	// doesn't exist (Proxmox returns 500 with "does not exist").
	if _, err := c.Pool(ctx, name); err == nil {
		return nil // pool already exists
	}

	return c.NewPool(ctx, name, "yage managed pool")
}

// splitTokenAuth splits a "tokenID=secret" string on the last '='.
// The tokenID may itself contain '=' characters so we split from the
// right. Returns ("", rawSecret) when no '=' is found.
func splitTokenAuth(raw string) (tokenID, secret string) {
	i := strings.LastIndex(raw, "=")
	if i < 0 {
		return "", raw
	}
	return raw[:i], raw[i+1:]
}

// minString returns the smallest string in s (stable equivalent of
// sorted(s)[0] for a non-empty slice).
func minString(s []string) string {
	m := s[0]
	for _, x := range s[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
