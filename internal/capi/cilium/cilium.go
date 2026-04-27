// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cilium hosts pure-logic Cilium helpers (CNI mode
// detection, LB-IPAM pool CIDR defaulting, manifest append).
package cilium

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
)

// NeedsKubeProxyReplacement ports cilium_needs_kube_proxy_replacement.
// CILIUM_KUBE_PROXY_REPLACEMENT is auto|true|false and interacts with
// CILIUM_INGRESS:
//   - auto / unset: follows CILIUM_INGRESS.
//   - true / yes / 1 / on: always true.
//   - false / no / 0 / off: false, but dies if CILIUM_INGRESS is true
//     (ingress requires the replacement).
//   - anything else: dies.
func NeedsKubeProxyReplacement(cfg *config.Config) bool {
	kpr := strings.ToLower(cfg.CiliumKubeProxyReplacement)
	switch kpr {
	case "auto", "":
		return sysinfo.IsTrue(cfg.CiliumIngress)
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		if sysinfo.IsTrue(cfg.CiliumIngress) {
			logx.Die("Cilium Ingress requires kube-proxy replacement — set CILIUM_KUBE_PROXY_REPLACEMENT to auto or true (not false).")
		}
		return false
	default:
		logx.Die("Invalid CILIUM_KUBE_PROXY_REPLACEMENT='%s' (use auto, true, or false).", cfg.CiliumKubeProxyReplacement)
		return false
	}
}

// DefaultLBIPAMPoolCIDRFromNodes ports default_cilium_lb_ipam_pool_cidr_from_nodes.
// Takes the first IP from the first range in NODE_IP_RANGES, combines it
// with IP_PREFIX to form a network, and returns the with_prefixlen string
// (the bash used Python's ipaddress.ip_network(..., strict=False)).
//
// Returns "" when inputs are insufficient or parsing fails — callers check
// for empty, like bash did (the Python block had a broad try/except).
func DefaultLBIPAMPoolCIDRFromNodes(cfg *config.Config) string {
	// bash: "$NODE_IP_RANGES | cut -d, -f1 | cut -d- -f1"
	ranges := cfg.NodeIPRanges
	firstRange := ranges
	if i := strings.IndexByte(ranges, ','); i >= 0 {
		firstRange = ranges[:i]
	}
	firstIP := firstRange
	if i := strings.IndexByte(firstRange, '-'); i >= 0 {
		firstIP = firstRange[:i]
	}
	firstIP = strings.TrimSpace(firstIP)
	prefix := strings.TrimSpace(cfg.IPPrefix)
	if firstIP == "" || prefix == "" {
		return ""
	}
	// Mimic Python's ip_network(..., strict=False): build a CIDR and mask
	// the address down to the network boundary.
	ip := net.ParseIP(firstIP)
	if ip == nil {
		return ""
	}
	cidrLen, err := parseUnsigned(prefix)
	if err != nil {
		return ""
	}
	is4 := ip.To4() != nil
	bits := 128
	if is4 {
		bits = 32
		ip = ip.To4()
	}
	if cidrLen < 0 || cidrLen > bits {
		return ""
	}
	mask := net.CIDRMask(cidrLen, bits)
	network := ip.Mask(mask)
	return fmt.Sprintf("%s/%d", network.String(), cidrLen)
}

// parseUnsigned parses a non-negative decimal int without the surprises of
// strconv (no sign, no leading whitespace tolerance).
func parseUnsigned(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q in %q", c, s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// AppendLBIPAMPoolManifest ports append_cilium_lb_ipam_pool_manifest.
// Appends a CiliumLoadBalancerIPPool document to manifestPath using either
// an explicit range (--cilium-lb-ipam-pool-start/--stop) or a CIDR block
// (default: derived from the node network). Does nothing if CILIUM_LB_IPAM
// is false, the manifest file doesn't exist, or no pool config is set.
func AppendLBIPAMPoolManifest(cfg *config.Config, manifestPath string) {
	if !sysinfo.IsTrue(cfg.CiliumLBIPAM) {
		return
	}
	if _, err := os.Stat(manifestPath); err != nil {
		// Matches bash: only append to an existing file.
		return
	}

	poolCIDR := cfg.CiliumLBIPAMPoolCIDR
	poolStart := cfg.CiliumLBIPAMPoolStart
	poolStop := cfg.CiliumLBIPAMPoolStop

	if poolCIDR == "" && poolStart == "" {
		poolCIDR = DefaultLBIPAMPoolCIDRFromNodes(cfg)
	}

	if (poolStart != "" && poolStop == "") || (poolStart == "" && poolStop != "") {
		logx.Die("Cilium LB-IPAM pool range requires both --cilium-lb-ipam-pool-start and --cilium-lb-ipam-pool-stop.")
	}

	if poolCIDR == "" && poolStart == "" {
		return
	}

	poolName := cfg.CiliumLBIPAMPoolName
	if poolName == "" {
		poolName = cfg.WorkloadClusterName + "-lb-pool"
	}

	// Open for append; caller has guaranteed the file exists.
	f, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logx.Die("Cannot append CiliumLoadBalancerIPPool to %s: %v", manifestPath, err)
	}
	defer f.Close()

	// Same literal output as the bash heredoc — trailing newline placement
	// included.
	fmt.Fprintf(f, `
---
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata:
  name: "%s"
spec:
  allowFirstLastIPs: "No"
  blocks:
`, poolName)

	if poolStart != "" && poolStop != "" {
		fmt.Fprintf(f, "    - start: \"%s\"\n      stop: \"%s\"\n", poolStart, poolStop)
	}
	if poolCIDR != "" {
		fmt.Fprintf(f, "    - cidr: \"%s\"\n", poolCIDR)
	}
}