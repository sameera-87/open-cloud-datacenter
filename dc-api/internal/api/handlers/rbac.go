// Package handlers — rbac.go
//
// requireTenantRole is the handler-level shortcut for RBAC enforcement.
//
// Every mutating handler calls this after extracting tenantID from context.
// Read-only (GET/LIST) handlers skip it — the existing tenant_id WHERE clause
// in the SQL queries already provides isolation.
//
// Scope chain length is 1 in M1.5 (just the tenant). When M5 lands, callers
// will pass a longer chain (subscription → resource_group → resource); the
// helper signature is already shaped for that by accepting tenantID as a
// parameter from which it builds the chain internally.
package handlers

import (
	"errors"
	"net/http"

	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// requireTenantRole enforces that the requesting principal holds at least the
// minimum role on the given tenant scope.
//
// It pulls the principal (type + id) and isAdmin flag from the request context
// (set by the auth middleware in Chunk 3), builds a single-element tenant scope
// chain, and delegates to rbac.RequireRole.
//
// Return value: true means "allowed, continue". false means "denied, stop" —
// the helper has already written a 401, 403, or 500 response to w.
//
// Usage pattern in a mutating handler:
//
//	tenantID, ok := middleware.TenantFromContext(r.Context())
//	if !ok {
//	    writeError(w, http.StatusUnauthorized, "no tenant in context")
//	    return
//	}
//	if !requireTenantRole(w, r, h.repo, tenantID, models.RoleMember) {
//	    return
//	}
//	// ... normal handler logic
func requireTenantRole(
	w http.ResponseWriter,
	r *http.Request,
	repo rbac.Repo,
	tenantID string,
	min models.Role,
) bool {
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return false
	}
	isAdmin := middleware.IsAdminFromContext(r.Context())

	chain := []models.Scope{{Type: models.ScopeTypeTenant, ID: tenantID}}
	if err := rbac.RequireRole(r.Context(), repo, pType, pID, isAdmin, chain, min); err != nil {
		if errors.Is(err, rbac.ErrInsufficientRole) {
			writeError(w, http.StatusForbidden, "insufficient role for this action")
			return false
		}
		// DB error or unexpected — log not available here, return 500.
		writeError(w, http.StatusInternalServerError, "rbac check failed")
		return false
	}
	return true
}
