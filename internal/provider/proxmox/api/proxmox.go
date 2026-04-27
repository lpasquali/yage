// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package api is the low-level Proxmox VE HTTP client and helper
// suite (URL parsing, token shape, region/node decoding, identity
// hashing). It is the implementation layer that the Provider
// abstraction (internal/provider/proxmox) sits on top of.
//
// Direct importers fall in two camps:
//   - `internal/provider/proxmox/`: the Provider plugin uses these
//     helpers to satisfy the Provider interface.
//   - Orchestrator-side packages (`internal/orchestrator`,
//     `cluster/kindsync`, `capi/caaph`, `capi/manifest`,
//     `platform/opentofux`): use the helpers directly during phases
//     that don't go through the Provider interface.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/capi/cilium"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// Default user "bases" used when suffixes are derived from CLUSTER_SET_ID.
const (
	DefaultCSIUserBase  = "kubernetes-csi@pve"
	DefaultCAPIUserBase = "capmox@pve"
)

var (
	nonIdentityRE = regexp.MustCompile(`[^a-z0-9]+`)
	numericRE     = regexp.MustCompile(`^[0-9]+$`)
)

// DeriveIdentitySuffix derives a 12-char Proxmox identity suffix from
// the source ID: lowercases, strips non-[a-z0-9] characters, truncates
// to 12. Dies if the result is empty.
func DeriveIdentitySuffix(sourceID string) string {
	lower := strings.ToLower(sourceID)
	compact := nonIdentityRE.ReplaceAllString(lower, "")
	if compact == "" {
		logx.Die("Cannot derive a Proxmox identity suffix from CLUSTER_SET_ID='%s'.", sourceID)
	}
	if len(compact) > 12 {
		compact = compact[:12]
	}
	return compact
}

// UserIDWithSuffix builds a suffixed user ID. Supports both
// "user@realm" and bare "user" bases.
func UserIDWithSuffix(userBase, suffix string) string {
	if i := strings.Index(userBase, "@"); i >= 0 {
		user := userBase[:i]
		realm := userBase[i+1:]
		return fmt.Sprintf("%s-%s@%s", user, suffix, realm)
	}
	return fmt.Sprintf("%s-%s", userBase, suffix)
}

// TokenName returns the Proxmox token name for the given prefix and
// the current IDENTITY_SUFFIX.
func TokenName(tokenPrefix, suffix string) string {
	return fmt.Sprintf("%s-%s", tokenPrefix, suffix)
}

// TokenNameForSet returns the Proxmox token name for the given prefix
// and explicit cluster-set id.
func TokenNameForSet(tokenPrefix, setID string) string {
	return fmt.Sprintf("%s-%s", tokenPrefix, setID)
}

// TokenID returns the full Proxmox token id ("<user>!<token-name>").
func TokenID(userID, tokenPrefix, suffix string) string {
	return fmt.Sprintf("%s!%s", userID, TokenName(tokenPrefix, suffix))
}

// TokenIDForSet returns the full Proxmox token id using an explicit
// cluster-set id.
func TokenIDForSet(userID, tokenPrefix, setID string) string {
	return fmt.Sprintf("%s!%s", userID, TokenNameForSet(tokenPrefix, setID))
}

// RefreshDerivedIdentityUserIDs fills empty PROXMOX_CSI_USER_ID /
// PROXMOX_CAPI_USER_ID from the defaults + IDENTITY_SUFFIX.
func RefreshDerivedIdentityUserIDs(cfg *config.Config) {
	if cfg.Providers.Proxmox.CSIUserID == "" {
		cfg.Providers.Proxmox.CSIUserID = UserIDWithSuffix(DefaultCSIUserBase, cfg.Providers.Proxmox.IdentitySuffix)
	}
	if cfg.Providers.Proxmox.CAPIUserID == "" {
		cfg.Providers.Proxmox.CAPIUserID = UserIDWithSuffix(DefaultCAPIUserBase, cfg.Providers.Proxmox.IdentitySuffix)
	}
}

// RefreshDerivedIdentityTokenIDs fills derived token IDs from the
// configured user IDs + token prefixes. Only fabricates token IDs
// when both id AND secret are empty — otherwise a derived id paired
// with a real secret (from kind) produces a 401.
func RefreshDerivedIdentityTokenIDs(cfg *config.Config) {
	RefreshDerivedIdentityUserIDs(cfg)
	if cfg.Providers.Proxmox.CAPIToken == "" && cfg.Providers.Proxmox.CAPISecret == "" &&
		cfg.Providers.Proxmox.CAPIUserID != "" && cfg.Providers.Proxmox.CAPITokenPrefix != "" {
		cfg.Providers.Proxmox.CAPIToken = TokenID(cfg.Providers.Proxmox.CAPIUserID, cfg.Providers.Proxmox.CAPITokenPrefix, cfg.Providers.Proxmox.IdentitySuffix)
	}
	if cfg.Providers.Proxmox.CSITokenID == "" && cfg.Providers.Proxmox.CSITokenSecret == "" &&
		cfg.Providers.Proxmox.CSIUserID != "" && cfg.Providers.Proxmox.CSITokenPrefix != "" {
		cfg.Providers.Proxmox.CSITokenID = TokenID(cfg.Providers.Proxmox.CSIUserID, cfg.Providers.Proxmox.CSITokenPrefix, cfg.Providers.Proxmox.IdentitySuffix)
	}
}

// DeriveCiliumClusterID derives a Cilium cluster id (1..255) from a
// CLUSTER_SET_ID. Delegates to cilium.DeriveClusterID.
func DeriveCiliumClusterID(sourceID string) string {
	return cilium.DeriveClusterID(sourceID)
}

// RefreshDerivedCiliumClusterID fills WorkloadCiliumClusterID from
// ClusterSetID when it is empty.
func RefreshDerivedCiliumClusterID(cfg *config.Config) {
	if cfg.WorkloadCiliumClusterID == "" {
		cfg.WorkloadCiliumClusterID = DeriveCiliumClusterID(cfg.ClusterSetID)
	}
}

// APIJSONURL appends /api2/json to cfg.Providers.Proxmox.URL unless
// it is already suffixed, stripping a trailing slash first.
func APIJSONURL(cfg *config.Config) string {
	u := cfg.Providers.Proxmox.URL
	if strings.HasSuffix(u, "/api2/json") {
		return u
	}
	return strings.TrimRight(u, "/") + "/api2/json"
}

// HostBaseURL strips a trailing /api2/json from the configured URL so
// callers can append the path portion themselves.
func HostBaseURL(cfg *config.Config) string {
	u := strings.TrimRight(cfg.Providers.Proxmox.URL, "/")
	if strings.HasSuffix(u, "/api2/json") {
		u = strings.TrimSuffix(u, "/api2/json")
	}
	return u
}

// NormalizeTokenSecret strips token-id prefixes from token-secret
// values: handles the "<token_id>=<secret>" and "...=secret" formats
// some providers return.
func NormalizeTokenSecret(rawSecret, tokenID string) string {
	if tokenID != "" && strings.HasPrefix(rawSecret, tokenID+"=") {
		return strings.TrimPrefix(rawSecret, tokenID+"=")
	}
	if i := strings.LastIndex(rawSecret, "="); i >= 0 {
		return rawSecret[i+1:]
	}
	return rawSecret
}

// ValidateTokenSecret dies when the secret is empty or still carries
// a "<token_id>=" prefix.
func ValidateTokenSecret(label, secret string) {
	if secret == "" {
		logx.Die("%s is empty after normalization.", label)
	}
	if strings.Contains(secret, "=") {
		logx.Die("%s is malformed (contains '='). It should be only the token secret value.", label)
	}
}

var (
	uuidV4RE       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	twelveHexRE    = regexp.MustCompile(`^[0-9a-f]{12}$`)
	numericFieldRE = regexp.MustCompile(`^[0-9]+$`)
)

// ValidateClusterSetIDFormat validates CLUSTER_SET_ID and dies on a
// malformed value (must be a positive integer, a UUID v4, or a
// 12-hex Proxmox identity suffix).
func ValidateClusterSetIDFormat(cfg *config.Config) {
	id := cfg.ClusterSetID
	switch {
	case numericFieldRE.MatchString(id):
		n, err := strconv.Atoi(id)
		if err != nil || n < 1 {
			logx.Die("Numeric CLUSTER_SET_ID must be >= 1.")
		}
	case uuidV4RE.MatchString(id):
		// ok
	case twelveHexRE.MatchString(id):
		// ok: 12-char compact suffix
	default:
		logx.Die("CLUSTER_SET_ID must be a positive integer, a UUID v4, or a 12-hex Proxmox identity suffix (recreate); got: %s", id)
	}
}

// InferIdentityFromTokenIDs infers Proxmox identity fields from the
// CSI / CAPI token IDs already in cfg. Returns true on successful
// inference and mutates cfg; false when the token ID strings are
// missing or do not share the same suffix.
func InferIdentityFromTokenIDs(cfg *config.Config) bool {
	if !strings.Contains(cfg.Providers.Proxmox.CSITokenID, "!") {
		return false
	}
	if !strings.Contains(cfg.Providers.Proxmox.CAPIToken, "!") {
		return false
	}
	csiUser, csiAfter, ok := strings.Cut(cfg.Providers.Proxmox.CSITokenID, "!")
	if !ok {
		return false
	}
	capiUser, capiAfter, ok := strings.Cut(cfg.Providers.Proxmox.CAPIToken, "!")
	if !ok {
		return false
	}

	csiPrefix, csiSuffix, okCSI := splitPrefixSuffix(csiAfter)
	if !okCSI {
		return false
	}
	capiPrefix, capiSuffix, okCAPI := splitPrefixSuffix(capiAfter)
	if !okCAPI {
		return false
	}

	if csiSuffix == "" || capiSuffix == "" || csiSuffix != capiSuffix {
		return false
	}

	cfg.Providers.Proxmox.CSIUserID = csiUser
	cfg.Providers.Proxmox.CSITokenPrefix = csiPrefix
	cfg.Providers.Proxmox.CAPIUserID = capiUser
	cfg.Providers.Proxmox.CAPITokenPrefix = capiPrefix
	cfg.Providers.Proxmox.IdentitySuffix = csiSuffix
	if cfg.ClusterSetID == "" {
		cfg.ClusterSetID = csiSuffix
	}
	return true
}

// splitPrefixSuffix splits "<prefix>-<suffix>" on the FIRST '-' and returns
// ("prefix", "suffix", true). Matches bash's `${a%%-*}` / `${a#"${pfx}"-}`.
func splitPrefixSuffix(s string) (string, string, bool) {
	i := strings.IndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// HashIdentityNameTag returns a short, stable hash tag for a manifest spec —
// used by CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV=true to append "-t<8hex>"
// to ProxmoxMachineTemplate names. SHA-256 prefix.
func HashIdentityNameTag(spec []byte) string {
	sum := sha256.Sum256(spec)
	return hex.EncodeToString(sum[:])[:8]
}

// HTTP-backed helpers (PVE API calls) live in http.go:
//   - ResolveAvailableClusterSetIDForRoles
//   - ResolveRegionAndNodeFromPVEAuth / FromAdminAPI / FromClusterctlAPI
//   - CheckAdminAPIConnectivity