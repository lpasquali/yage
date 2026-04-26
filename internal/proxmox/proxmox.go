// Package proxmox ports Proxmox identity / token / URL helpers from
// the original bash port. Pure-logic helpers (no HTTP, no exec) live here.
// HTTP-backed helpers (resolve_available_cluster_set_id_for_roles,
// _resolve_proxmox_region_and_node_from_pve_auth_value) are stubbed until
// their calling phases are ported — they need the management cluster /
// Proxmox API to be reachable, so adding them without the orchestration
// around them would not be useful.
package proxmox

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"regexp"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/logx"
)

// Default user "bases" used when suffixes are derived from CLUSTER_SET_ID.
// Match the bash DEFAULT_PROXMOX_*_USER_BASE constants (L1241-L1242).
const (
	DefaultCSIUserBase  = "kubernetes-csi@pve"
	DefaultCAPIUserBase = "capmox@pve"
)

// GenerateUUIDv4 ports generate_uuid_v4(). Uses crypto/rand directly; the
// bash fallback that reads /dev/urandom is unnecessary because Go's rand
// package already does that.
func GenerateUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Deterministic fallback so tests / offline runs don't panic; in
		// practice crypto/rand on Linux reads from getrandom(2) and never
		// fails. Matches bash's od fallback intent.
		seed := make([]byte, 16)
		binary.BigEndian.PutUint64(seed, uint64(42))
		copy(b[:], seed)
	}
	// Set version v4 and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

var nonIdentityRE = regexp.MustCompile(`[^a-z0-9]+`)

// DeriveIdentitySuffix ports derive_proxmox_identity_suffix. Lowercases,
// strips non-[a-z0-9] characters, truncates to 12. Dies if empty.
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

// UserIDWithSuffix ports proxmox_user_id_with_suffix. Supports both
// "user@realm" and bare "user" bases.
func UserIDWithSuffix(userBase, suffix string) string {
	if i := strings.Index(userBase, "@"); i >= 0 {
		user := userBase[:i]
		realm := userBase[i+1:]
		return fmt.Sprintf("%s-%s@%s", user, suffix, realm)
	}
	return fmt.Sprintf("%s-%s", userBase, suffix)
}

// TokenName ports proxmox_token_name (uses the current IDENTITY_SUFFIX).
func TokenName(tokenPrefix, suffix string) string {
	return fmt.Sprintf("%s-%s", tokenPrefix, suffix)
}

// TokenNameForSet ports proxmox_token_name_for_set (explicit set id).
func TokenNameForSet(tokenPrefix, setID string) string {
	return fmt.Sprintf("%s-%s", tokenPrefix, setID)
}

// TokenID ports proxmox_token_id.
func TokenID(userID, tokenPrefix, suffix string) string {
	return fmt.Sprintf("%s!%s", userID, TokenName(tokenPrefix, suffix))
}

// TokenIDForSet ports proxmox_token_id_for_set.
func TokenIDForSet(userID, tokenPrefix, setID string) string {
	return fmt.Sprintf("%s!%s", userID, TokenNameForSet(tokenPrefix, setID))
}

// RefreshDerivedIdentityUserIDs ports refresh_derived_identity_user_ids.
// Fills empty PROXMOX_CSI_USER_ID / PROXMOX_CAPI_USER_ID from the defaults
// + IDENTITY_SUFFIX.
func RefreshDerivedIdentityUserIDs(cfg *config.Config) {
	if cfg.ProxmoxCSIUserID == "" {
		cfg.ProxmoxCSIUserID = UserIDWithSuffix(DefaultCSIUserBase, cfg.ProxmoxIdentitySuffix)
	}
	if cfg.ProxmoxCAPIUserID == "" {
		cfg.ProxmoxCAPIUserID = UserIDWithSuffix(DefaultCAPIUserBase, cfg.ProxmoxIdentitySuffix)
	}
}

// RefreshDerivedIdentityTokenIDs ports refresh_derived_identity_token_ids.
// Only fabricates token IDs when both id AND secret are empty — otherwise a
// derived id paired with a real secret (from kind) produces a 401.
func RefreshDerivedIdentityTokenIDs(cfg *config.Config) {
	RefreshDerivedIdentityUserIDs(cfg)
	if cfg.ProxmoxToken == "" && cfg.ProxmoxSecret == "" &&
		cfg.ProxmoxCAPIUserID != "" && cfg.ProxmoxCAPITokenPrefix != "" {
		cfg.ProxmoxToken = TokenID(cfg.ProxmoxCAPIUserID, cfg.ProxmoxCAPITokenPrefix, cfg.ProxmoxIdentitySuffix)
	}
	if cfg.ProxmoxCSITokenID == "" && cfg.ProxmoxCSITokenSecret == "" &&
		cfg.ProxmoxCSIUserID != "" && cfg.ProxmoxCSITokenPrefix != "" {
		cfg.ProxmoxCSITokenID = TokenID(cfg.ProxmoxCSIUserID, cfg.ProxmoxCSITokenPrefix, cfg.ProxmoxIdentitySuffix)
	}
}

var numericRE = regexp.MustCompile(`^[0-9]+$`)

// DeriveCiliumClusterID ports derive_cilium_cluster_id. For numeric
// CLUSTER_SET_IDs returns the id modulo 255 + 1; otherwise uses BSD cksum
// of the id string. Result is 1..255.
//
// bash `cksum` is POSIX CRC32 over the input with the length appended; Go's
// hash/crc32 with the IEEE table matches the first 32-bit CRC bash prints.
// The modulo arithmetic keeps the result in [1, 255].
func DeriveCiliumClusterID(sourceID string) string {
	var derived uint64
	if numericRE.MatchString(sourceID) {
		// bash "derived=$source_id"; big numbers fold through the modulo.
		n, err := strconv.ParseUint(sourceID, 10, 64)
		if err == nil {
			derived = n
		}
	} else {
		derived = uint64(crc32.ChecksumIEEE([]byte(sourceID)))
	}
	return strconv.FormatUint((derived%255)+1, 10)
}

// RefreshDerivedCiliumClusterID ports refresh_derived_cilium_cluster_id.
func RefreshDerivedCiliumClusterID(cfg *config.Config) {
	if cfg.WorkloadCiliumClusterID == "" {
		cfg.WorkloadCiliumClusterID = DeriveCiliumClusterID(cfg.ClusterSetID)
	}
}

// APIJSONURL ports proxmox_api_json_url: appends /api2/json unless already
// suffixed, stripping a trailing slash first.
func APIJSONURL(cfg *config.Config) string {
	u := cfg.ProxmoxURL
	if strings.HasSuffix(u, "/api2/json") {
		return u
	}
	return strings.TrimRight(u, "/") + "/api2/json"
}

// HostBaseURL ports pve_api_host_base_url: strips a trailing /api2/json so
// callers can append the path portion themselves.
func HostBaseURL(cfg *config.Config) string {
	u := strings.TrimRight(cfg.ProxmoxURL, "/")
	if strings.HasSuffix(u, "/api2/json") {
		u = strings.TrimSuffix(u, "/api2/json")
	}
	return u
}

// NormalizeTokenSecret ports normalize_proxmox_token_secret: handles the
// "<token_id>=<secret>" and "...=secret" return formats some providers use.
func NormalizeTokenSecret(rawSecret, tokenID string) string {
	if tokenID != "" && strings.HasPrefix(rawSecret, tokenID+"=") {
		return strings.TrimPrefix(rawSecret, tokenID+"=")
	}
	if i := strings.LastIndex(rawSecret, "="); i >= 0 {
		return rawSecret[i+1:]
	}
	return rawSecret
}

// ValidateTokenSecret ports validate_proxmox_token_secret. Dies on failure.
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

// ValidateClusterSetIDFormat ports validate_cluster_set_id_format. Dies
// with the same error message as bash on an invalid CLUSTER_SET_ID.
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

// InferIdentityFromTokenIDs ports infer_proxmox_identity_from_token_ids.
// Returns true on successful inference and mutates cfg; false when the
// token ID strings are missing or do not share the same suffix.
func InferIdentityFromTokenIDs(cfg *config.Config) bool {
	if !strings.Contains(cfg.ProxmoxCSITokenID, "!") {
		return false
	}
	if !strings.Contains(cfg.ProxmoxToken, "!") {
		return false
	}
	csiUser, csiAfter, ok := strings.Cut(cfg.ProxmoxCSITokenID, "!")
	if !ok {
		return false
	}
	capiUser, capiAfter, ok := strings.Cut(cfg.ProxmoxToken, "!")
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

	cfg.ProxmoxCSIUserID = csiUser
	cfg.ProxmoxCSITokenPrefix = csiPrefix
	cfg.ProxmoxCAPIUserID = capiUser
	cfg.ProxmoxCAPITokenPrefix = capiPrefix
	cfg.ProxmoxIdentitySuffix = csiSuffix
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
// to ProxmoxMachineTemplate names.
//
// Not in the bash section above but ports the SHA-256 prefix in the
// companion comment block (L178-L181).
func HashIdentityNameTag(spec []byte) string {
	sum := sha256.Sum256(spec)
	return hex.EncodeToString(sum[:])[:8]
}

// HTTP-backed helpers (PVE API calls) live in http.go:
//   - ResolveAvailableClusterSetIDForRoles
//   - ResolveRegionAndNodeFromPVEAuth / FromAdminAPI / FromClusterctlAPI
//   - CheckAdminAPIConnectivity
