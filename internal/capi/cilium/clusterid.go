// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cilium

import (
	"hash/crc32"
	"regexp"
	"strconv"
)

var numericClusterIDRE = regexp.MustCompile(`^\d+$`)

// DeriveClusterID maps an arbitrary string ID to a stable Cilium
// cluster-id in the range [1, 255]. Numeric IDs are used directly
// (mod 255 + 1); all other IDs are hashed with CRC32/IEEE.
func DeriveClusterID(sourceID string) string {
	var derived uint64
	if numericClusterIDRE.MatchString(sourceID) {
		n, _ := strconv.ParseUint(sourceID, 10, 64)
		derived = n
	} else {
		derived = uint64(crc32.ChecksumIEEE([]byte(sourceID)))
	}
	return strconv.FormatUint((derived%255)+1, 10)
}
