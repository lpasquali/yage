// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
)

// httpClient returns a client honouring PROXMOX_ADMIN_INSECURE: when
// insecure, TLS verification is skipped.
func httpClient(insecure bool) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: tr}
}

// fetchJSON issues a GET with the PVEAPIToken auth header. The authValue
// must be the full header value (including the "PVEAPIToken=" prefix);
// when the prefix is missing, fetchJSON adds it.
func fetchJSON(u, authValue string, insecure bool, out any) error {
	if !strings.HasPrefix(strings.ToLower(authValue), "pveapitoken=") {
		authValue = "PVEAPIToken=" + authValue
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authValue)
	resp, err := httpClient(insecure).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// statusCode returns the HTTP status for a GET on u, or 0 on network
// failure. Matches `curl -sk -o /dev/null -w "%{http_code}"` with the
// "000" sentinel collapsed to 0.
func statusCode(u, authValue string, insecure bool) int {
	if !strings.HasPrefix(strings.ToLower(authValue), "pveapitoken=") {
		authValue = "PVEAPIToken=" + authValue
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", authValue)
	resp, err := httpClient(insecure).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ResolveRegionAndNodeFromPVEAuth fills empty
// cfg.Providers.Proxmox.Node and cfg.Providers.Proxmox.Region by calling
//
//	GET /api2/json/nodes           (for local/all nodes)
//	GET /api2/json/cluster/status  (for cluster name)
//
// authValue is the full PVEAPIToken header value (the function also
// accepts one without the prefix and adds it). Silent no-op when already
// populated, URL is empty, or auth is empty.
func ResolveRegionAndNodeFromPVEAuth(cfg *config.Config, authValue string) error {
	if cfg.Providers.Proxmox.URL == "" || authValue == "" {
		return nil
	}
	if cfg.Providers.Proxmox.Region != "" && cfg.Providers.Proxmox.Node != "" {
		return nil
	}
	base := strings.TrimRight(APIJSONURL(cfg), "/")
	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)

	if cfg.Providers.Proxmox.Node == "" {
		var np struct {
			Data []struct {
				Node  string `json:"node"`
				Local any    `json:"local"`
			} `json:"data"`
		}
		if err := fetchJSON(base+"/nodes", authValue, insecure, &np); err == nil {
			var locals, all []string
			for _, n := range np.Data {
				if n.Node == "" {
					continue
				}
				all = append(all, n.Node)
				// "local" is often 1/true; coerce to a stable string.
				s := fmt.Sprint(n.Local)
				if s == "1" || s == "true" {
					locals = append(locals, n.Node)
				}
			}
			switch {
			case len(locals) > 0:
				cfg.Providers.Proxmox.Node = locals[0]
			case len(all) > 0:
				// Sort for deterministic pick.
				cfg.Providers.Proxmox.Node = minString(all)
			}
		}
	}

	if cfg.Providers.Proxmox.Region == "" {
		var cp struct {
			Data []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := fetchJSON(base+"/cluster/status", authValue, insecure, &cp); err == nil {
			for _, it := range cp.Data {
				if it.Type == "cluster" && it.Name != "" {
					cfg.Providers.Proxmox.Region = it.Name
					break
				}
			}
		}
		if cfg.Providers.Proxmox.Region == "" && cfg.Providers.Proxmox.Node != "" {
			cfg.Providers.Proxmox.Region = cfg.Providers.Proxmox.Node
		}
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
// Dies on any non-200.
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

	base := HostBaseURL(cfg)
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.Providers.Proxmox.AdminUsername, cfg.Providers.Proxmox.AdminToken)
	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)

	logx.Log("Validating Proxmox admin API credentials at %s...", base)
	switch code := statusCode(base+"/api2/json/version", auth, insecure); code {
	case 200:
		logx.Log("Proxmox admin API token validated (HTTP 200 on /version).")
	case 401:
		logx.Die("Proxmox admin API token unauthorized (401). Check PROXMOX_ADMIN_USERNAME token ID and PROXMOX_ADMIN_TOKEN secret.")
	case 0:
		logx.Die("Could not reach Proxmox API at %s. Check PROXMOX_URL and network connectivity.", base)
	default:
		logx.Die("Unexpected HTTP %d while validating admin token at %s.", code, base)
	}

	switch code := statusCode(base+"/api2/json/access/roles", auth, insecure); code {
	case 200:
		logx.Log("Proxmox admin token can access /access/roles (required for role bootstrap).")
	case 401:
		logx.Die("Proxmox admin token cannot access /access/roles (401). Token lacks required privileges for OpenTofu role creation.")
	default:
		logx.Die("Unexpected HTTP %d while checking /access/roles permissions for OpenTofu orchestrator.", code)
	}
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
	base := strings.TrimRight(APIJSONURL(cfg), "/")
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.Providers.Proxmox.AdminUsername, cfg.Providers.Proxmox.AdminToken)
	insecure := sysinfo.IsTrue(cfg.Providers.Proxmox.AdminInsecure)

	type rolesResp struct {
		Data []struct {
			RoleID string `json:"roleid"`
		} `json:"data"`
	}
	type usersResp struct {
		Data []struct {
			UserID string `json:"userid"`
		} `json:"data"`
	}
	type tokenResp struct {
		Data []struct {
			TokenID string `json:"tokenid"`
		} `json:"data"`
	}

	var roles rolesResp
	var users usersResp
	if err := fetchJSON(base+"/access/roles", auth, insecure, &roles); err != nil {
		logx.Warn("Failed to compute an available CLUSTER_SET_ID from Proxmox identity inventory; using explicit CLUSTER_SET_ID=%s.", cfg.ClusterSetID)
		return err
	}
	if err := fetchJSON(base+"/access/users", auth, insecure, &users); err != nil {
		logx.Warn("Failed to compute an available CLUSTER_SET_ID from Proxmox identity inventory; using explicit CLUSTER_SET_ID=%s.", cfg.ClusterSetID)
		return err
	}
	roleSet := map[string]bool{}
	for _, r := range roles.Data {
		roleSet[r.RoleID] = true
	}
	userSet := map[string]bool{}
	for _, u := range users.Data {
		userSet[u.UserID] = true
	}

	tokenNameExists := func(userID, name string) bool {
		if !userSet[userID] {
			return false
		}
		var t tokenResp
		u := base + "/access/users/" + url.PathEscape(userID) + "/token"
		if err := fetchJSON(u, auth, insecure, &t); err != nil {
			return false
		}
		full := userID + "!" + name
		for _, it := range t.Data {
			if it.TokenID == full {
				return true
			}
			if i := strings.IndexByte(it.TokenID, '!'); i >= 0 && it.TokenID[i+1:] == name {
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
// present (Proxmox returns 500 with "already exists" on duplicate
// POST). Uses the admin token (the clusterctl token usually doesn't
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
	base := strings.TrimRight(HostBaseURL(cfg), "/")
	if base == "" {
		return fmt.Errorf("EnsurePool %s: PROXMOX_URL is empty", name)
	}
	auth := "PVEAPIToken=" + cfg.Providers.Proxmox.AdminUsername + "=" + cfg.Providers.Proxmox.AdminToken

	// Idempotency probe: GET /pools/<name> — 200 means it exists.
	if statusCode(base+"/api2/json/pools/"+name, auth, isInsecure(cfg.Providers.Proxmox.AdminInsecure)) == 200 {
		return nil
	}

	// Create. Use POST /api2/json/pools with form body poolid=<name>.
	body := strings.NewReader("poolid=" + name)
	req, err := http.NewRequest(http.MethodPost, base+"/api2/json/pools", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := httpClient(isInsecure(cfg.Providers.Proxmox.AdminInsecure))
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil
	}
	// Some Proxmox versions return 500 with text body containing
	// "already exists" on duplicate poolid — treat as success.
	bb, _ := io.ReadAll(resp.Body)
	if strings.Contains(strings.ToLower(string(bb)), "already exists") {
		return nil
	}
	return fmt.Errorf("create pool %s: HTTP %d: %s", name, resp.StatusCode, string(bb))
}

func isInsecure(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "y", "on":
		return true
	}
	return false
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