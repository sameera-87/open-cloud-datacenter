package admin

import (
	"encoding/json"
	"fmt"
)

// apiErrorf renders an actionable error string for a non-2xx API response.
// Handles the quota_exceeded body shape (with cap/allocated/available/requested)
// so admin output reads cleanly when a cap shrink is refused.
func apiErrorf(status int, body []byte) error {
	// Try the rich quota_exceeded shape first.
	var qe struct {
		Error     string `json:"error"`
		Message   string `json:"message"`
		TenantCap *struct {
			CPUCores  int `json:"cpu_cores"`
			MemoryGB  int `json:"memory_gb"`
			StorageGB int `json:"storage_gb"`
		} `json:"tenant_cap,omitempty"`
		Allocated *struct {
			CPUCores  int `json:"cpu_cores"`
			MemoryGB  int `json:"memory_gb"`
			StorageGB int `json:"storage_gb"`
		} `json:"allocated,omitempty"`
		Available *struct {
			CPUCores  int `json:"cpu_cores"`
			MemoryGB  int `json:"memory_gb"`
			StorageGB int `json:"storage_gb"`
		} `json:"available,omitempty"`
		Requested *struct {
			CPUCores  int `json:"cpu_cores"`
			MemoryGB  int `json:"memory_gb"`
			StorageGB int `json:"storage_gb"`
		} `json:"requested,omitempty"`
	}
	if err := json.Unmarshal(body, &qe); err == nil && qe.Error == "quota_exceeded" {
		s := fmt.Sprintf("HTTP %d quota_exceeded: %s", status, qe.Message)
		if qe.TenantCap != nil && qe.Allocated != nil {
			s += fmt.Sprintf("\n  Tenant cap:  %d cpu / %d GiB / %d GiB",
				qe.TenantCap.CPUCores, qe.TenantCap.MemoryGB, qe.TenantCap.StorageGB)
			s += fmt.Sprintf("\n  Allocated:   %d cpu / %d GiB / %d GiB",
				qe.Allocated.CPUCores, qe.Allocated.MemoryGB, qe.Allocated.StorageGB)
			if qe.Available != nil {
				s += fmt.Sprintf("\n  Available:   %d cpu / %d GiB / %d GiB",
					qe.Available.CPUCores, qe.Available.MemoryGB, qe.Available.StorageGB)
			}
		}
		if qe.Requested != nil {
			s += fmt.Sprintf("\n  Requested:   %d cpu / %d GiB / %d GiB",
				qe.Requested.CPUCores, qe.Requested.MemoryGB, qe.Requested.StorageGB)
		}
		return fmt.Errorf("%s", s)
	}

	// Fall back to a generic error body.
	var generic struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &generic); err == nil {
		msg := generic.Error
		if generic.Message != "" {
			msg = generic.Message
		}
		if msg != "" {
			return fmt.Errorf("HTTP %d: %s", status, msg)
		}
	}
	return fmt.Errorf("HTTP %d: %s", status, string(body))
}
