package kvi

import "encoding/base64"

// decodeBase64Maybe accepts a string that *might* be base64. Tries strict
// std-encoding first; on failure returns the raw string bytes.
func decodeBase64Maybe(s string) []byte {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	return []byte(s)
}
