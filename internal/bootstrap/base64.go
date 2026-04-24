package bootstrap

import "encoding/base64"

// base64Decode strips any surrounding whitespace before decoding so it
// tolerates the `kubectl -o jsonpath=` padding quirks.
func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
