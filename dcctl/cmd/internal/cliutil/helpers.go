// Package cliutil provides shared helper functions for all dcctl command packages.
package cliutil

import (
	"encoding/json"
	"fmt"
)

// APIErrorf builds a friendly error from a non-2xx response body. The body
// shape is the spec's Error schema (`{"error": "..."}`); fall back to raw
// text if it doesn't parse.
func APIErrorf(status int, body []byte) error {
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
		return fmt.Errorf("DC-API error (%d): %s", status, parsed.Error)
	}
	return fmt.Errorf("DC-API returned HTTP %d: %s", status, string(body))
}

// Deref returns "" when p is nil, *p otherwise.
func Deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// DerefOrDash returns "-" when p is nil or empty, *p otherwise.
func DerefOrDash(p *string) string {
	if p == nil || *p == "" {
		return "-"
	}
	return *p
}

// TruncTime renders a *time.Time-shaped string to its first 19 chars
// (YYYY-MM-DDTHH:MM:SS), dropping the timezone/fractional seconds for
// consistent column width in table output.
func TruncTime(s string) string {
	if len(s) > 19 {
		return s[:19]
	}
	return s
}

// OrDash returns "-" when s is empty, s otherwise.
func OrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Truncate shortens a string to at most maxLen chars, appending "..." if cut.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
