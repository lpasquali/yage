package proxmox

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
	"github.com/lpasquali/yage/internal/logx"
	"github.com/lpasquali/yage/internal/sysinfo"
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
// must be the full header value (including the "PVEAPIToken=" prefix)
// unless it already starts with that prefix — mirrors bash's auto-prefix.
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

// ResolveRegionAndNodeFromPVEAuth ports
// _resolve_proxmox_region_and_node_from_pve_auth_value (L3361-L3459).
// Fills empty cfg.ProxmoxNode and cfg.ProxmoxRegion by calling
//
//	GET /api2/json/nodes           (for local/all nodes)
//	GET /api2/json/cluster/status  (for cluster name)
//
// authValue is the full PVEAPIToken header value (the function also
// accepts one without the prefix and adds it). Silent no-op when already
// populated, URL is empty, or auth is empty.
func ResolveRegionAndNodeFromPVEAuth(cfg *config.Config, authValue string) error {
	if cfg.ProxmoxURL == "" || authValue == "" {
		return nil
	}
	if cfg.ProxmoxRegion != "" && cfg.ProxmoxNode != "" {
		return nil
	}
	base := strings.TrimRight(APIJSONURL(cfg), "/")
	insecure := sysinfo.IsTrue(cfg.ProxmoxAdminInsecure)

	if cfg.ProxmoxNode == "" {
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
				// "local" is often 1/true but bash coerces via str().lower().
				s := fmt.Sprint(n.Local)
				if s == "1" || s == "true" {
					locals = append(locals, n.Node)
				}
			}
			switch {
			case len(locals) > 0:
				cfg.ProxmoxNode = locals[0]
			case len(all) > 0:
				// Sort for deterministic pick (bash uses sorted()).
				cfg.ProxmoxNode = minString(all)
			}
		}
	}

	if cfg.ProxmoxRegion == "" {
		var cp struct {
			Data []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := fetchJSON(base+"/cluster/status", authValue, insecure, &cp); err == nil {
			for _, it := range cp.Data {
				if it.Type == "cluster" && it.Name != "" {
					cfg.ProxmoxRegion = it.Name
					break
				}
			}
		}
		if cfg.ProxmoxRegion == "" && cfg.ProxmoxNode != "" {
			cfg.ProxmoxRegion = cfg.ProxmoxNode
		}
	}

	if cfg.ProxmoxRegion != "" {
		logx.Log("Derived PROXMOX_REGION from Proxmox API: %s", cfg.ProxmoxRegion)
	}
	if cfg.ProxmoxNode != "" {
		logx.Log("Derived PROXMOX_NODE from Proxmox API: %s", cfg.ProxmoxNode)
	}
	return nil
}

// ResolveRegionAndNodeFromAdminAPI ports
// resolve_proxmox_region_and_node_from_admin_api (L3461-L3464).
func ResolveRegionAndNodeFromAdminAPI(cfg *config.Config) error {
	if cfg.ProxmoxAdminUsername == "" || cfg.ProxmoxAdminToken == "" {
		return nil
	}
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.ProxmoxAdminUsername, cfg.ProxmoxAdminToken)
	return ResolveRegionAndNodeFromPVEAuth(cfg, auth)
}

// ResolveRegionAndNodeFromClusterctlAPI ports
// resolve_proxmox_region_and_node_from_clusterctl_api (L3467-L3472).
// Uses PROXMOX_TOKEN + PROXMOX_SECRET (the CAPI token), normalising the
// secret first.
func ResolveRegionAndNodeFromClusterctlAPI(cfg *config.Config) error {
	if cfg.ProxmoxToken == "" || cfg.ProxmoxSecret == "" {
		return nil
	}
	sec := NormalizeTokenSecret(cfg.ProxmoxSecret, cfg.ProxmoxToken)
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.ProxmoxToken, sec)
	return ResolveRegionAndNodeFromPVEAuth(cfg, auth)
}

// CheckAdminAPIConnectivity ports check_proxmox_admin_api_connectivity
// (L3474-L3521). Validates:
//  1. GET /api2/json/version returns 200 (credentials valid)
//  2. GET /api2/json/access/roles returns 200 (admin has required
//     privileges for OpenTofu role bootstrap)
//
// Dies on any non-200 (matches bash `die` on 401/000/other).
func CheckAdminAPIConnectivity(cfg *config.Config) {
	if cfg.ProxmoxURL == "" {
		logx.Die("PROXMOX_URL is required for OpenTofu identity bootstrap.")
	}
	if cfg.ProxmoxAdminUsername == "" {
		logx.Die("PROXMOX_ADMIN_USERNAME is required for OpenTofu identity bootstrap.")
	}
	if cfg.ProxmoxAdminToken == "" {
		logx.Die("PROXMOX_ADMIN_TOKEN is required for OpenTofu identity bootstrap.")
	}

	base := HostBaseURL(cfg)
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.ProxmoxAdminUsername, cfg.ProxmoxAdminToken)
	insecure := sysinfo.IsTrue(cfg.ProxmoxAdminInsecure)

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
		logx.Die("Unexpected HTTP %d while checking /access/roles permissions for OpenTofu bootstrap.", code)
	}
}

// ResolveAvailableClusterSetIDForRoles ports
// resolve_available_cluster_set_id_for_roles (L1320-L1462).
// Only applies to numeric CLUSTER_SET_IDs — UUIDs are unique enough and
// skip this search. Walks PVE /access/roles, /access/users, and per-user
// /access/users/<id>/token to find the first integer starting from
// cfg.ClusterSetID that has no collision for the derived role/user/token
// names. On conflict, updates cfg.ClusterSetID + identity suffix + derived
// user/token IDs.
func ResolveAvailableClusterSetIDForRoles(cfg *config.Config) error {
	if !numericRE.MatchString(cfg.ClusterSetID) {
		return nil
	}
	base := strings.TrimRight(APIJSONURL(cfg), "/")
	auth := fmt.Sprintf("PVEAPIToken=%s=%s", cfg.ProxmoxAdminUsername, cfg.ProxmoxAdminToken)
	insecure := sysinfo.IsTrue(cfg.ProxmoxAdminInsecure)

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

	csiPrefix := cfg.ProxmoxCSITokenPrefix
	capiPrefix := cfg.ProxmoxCAPITokenPrefix
	explicitCSIUser := strings.TrimSpace(cfg.ProxmoxCSIUserID)
	explicitCAPIUser := strings.TrimSpace(cfg.ProxmoxCAPIUserID)

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
			oldCSITokenID := TokenIDForSet(oldCSIUser, cfg.ProxmoxCSITokenPrefix, oldSuffix)
			oldCAPITokenID := TokenIDForSet(oldCAPIUser, cfg.ProxmoxCAPITokenPrefix, oldSuffix)

			logx.Warn("CLUSTER_SET_ID=%s is already in use in Proxmox identity resources; using CLUSTER_SET_ID=%s.", cfg.ClusterSetID, resolved)
			cfg.ClusterSetID = resolved
			cfg.ProxmoxIdentitySuffix = DeriveIdentitySuffix(cfg.ClusterSetID)

			if cfg.ProxmoxCSIUserID == "" || cfg.ProxmoxCSIUserID == oldCSIUser {
				cfg.ProxmoxCSIUserID = UserIDWithSuffix(DefaultCSIUserBase, cfg.ProxmoxIdentitySuffix)
			}
			if cfg.ProxmoxCAPIUserID == "" || cfg.ProxmoxCAPIUserID == oldCAPIUser {
				cfg.ProxmoxCAPIUserID = UserIDWithSuffix(DefaultCAPIUserBase, cfg.ProxmoxIdentitySuffix)
			}
			if cfg.ProxmoxCSITokenID == "" || cfg.ProxmoxCSITokenID == oldCSITokenID {
				cfg.ProxmoxCSITokenID = TokenID(cfg.ProxmoxCSIUserID, cfg.ProxmoxCSITokenPrefix, cfg.ProxmoxIdentitySuffix)
			}
			if cfg.ProxmoxToken == "" || cfg.ProxmoxToken == oldCAPITokenID {
				cfg.ProxmoxToken = TokenID(cfg.ProxmoxCAPIUserID, cfg.ProxmoxCAPITokenPrefix, cfg.ProxmoxIdentitySuffix)
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
// Bash equivalent: pveum pool add <name> ; we don't have pveum here,
// so we POST /api2/json/pools directly.
func EnsurePool(cfg *config.Config, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	if cfg.ProxmoxAdminUsername == "" || cfg.ProxmoxAdminToken == "" {
		return fmt.Errorf("EnsurePool %s: admin credentials missing (PROXMOX_ADMIN_USERNAME / PROXMOX_ADMIN_TOKEN)", name)
	}
	base := strings.TrimRight(HostBaseURL(cfg), "/")
	if base == "" {
		return fmt.Errorf("EnsurePool %s: PROXMOX_URL is empty", name)
	}
	auth := "PVEAPIToken=" + cfg.ProxmoxAdminUsername + "=" + cfg.ProxmoxAdminToken

	// Idempotency probe: GET /pools/<name> — 200 means it exists.
	if statusCode(base+"/api2/json/pools/"+name, auth, isInsecure(cfg.ProxmoxAdminInsecure)) == 200 {
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
	c := httpClient(isInsecure(cfg.ProxmoxAdminInsecure))
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
