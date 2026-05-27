// Package handlers — response.go
//
// HTTP response helpers shared across every handler in the package. Moved
// here from vm.go (the first handler shipped) so the helpers don't masquerade
// as VM-specific.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/wso2/dc-api/internal/models"
)

// writeJSON encodes v as JSON, sets the standard headers, and writes the
// status code.
//
// Cache-Control: no-store is sent on every response. Browsers cache
// 200/301/404/410 by default in the absence of an explicit policy, and our
// responses reflect dynamic state that can change between calls (resource
// phase, soft-delete toggles, quota counters, etc.). A real bug we already
// hit: a stale 410 surviving a KV secret restore. Cheaper to blanket
// no-store on every response than to whitelist per-route.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes a standard {"error": "..."} body with the given status.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeQuotaExceeded renders the canonical 400 body for a quota-exceeded
// rejection, with cap / allocated / available / requested fields so the
// caller can render an actionable error. usage may be nil if the caller
// doesn't have a TenantCapUsage on hand (in which case only `error`,
// `message`, and `requested` populate).
func writeQuotaExceeded(w http.ResponseWriter, message string, usage *models.TenantCapUsage, requested models.TenantCap) {
	body := map[string]interface{}{
		"error":     "quota_exceeded",
		"message":   message,
		"requested": requested,
	}
	if usage != nil {
		body["tenant_cap"] = usage.Cap
		body["allocated"] = usage.Allocated
		body["available"] = usage.Available
	}
	writeJSON(w, http.StatusBadRequest, body)
}
