package openbao

import (
	"context"
	"fmt"
	"net/http"
)

// ── Token operations ────────────────────────────────────────────────────────
//
// dc-api needs to talk to OpenBao for human-driven secret CRUD. The naive
// path is to hand it the root token, but that's god-mode (rekey, sys/init,
// arbitrary mount creation, …). Instead we mint a token bound to a scoped
// "dc-api-admin" policy that only grants what dc-api actually needs.
//
// Reference:
//   https://openbao.org/api-docs/auth/token/#create-token

// CreateTokenRequest mirrors the subset of POST /v1/auth/token/create that
// this controller uses. Many fields exist; we only set what we depend on.
//
// Periodic tokens (set Period != "") auto-renew at every use up to the
// period duration — effectively immortal as long as the holder uses them
// at least once per period. Preferred over a high TTL because there's no
// hard expiry date to remember, and a leaked token still loses access if
// the period elapses without renewal (defence in depth vs naive long TTL).
type CreateTokenRequest struct {
	Policies        []string `json:"policies,omitempty"`
	TTL             string   `json:"ttl,omitempty"`     // explicit TTL; usually empty when Period is set
	Period          string   `json:"period,omitempty"`  // periodic token (auto-renew window)
	Renewable       bool     `json:"renewable"`
	NoDefaultPolicy bool     `json:"no_default_policy"` // true → only `policies` apply, no `default` policy
	DisplayName     string   `json:"display_name,omitempty"`
}

// CreateTokenResponse is what OpenBao returns. We only consume the
// client_token.
type CreateTokenResponse struct {
	Auth struct {
		ClientToken string   `json:"client_token"`
		Policies    []string `json:"policies"`
	} `json:"auth"`
}

// CreateToken calls POST /v1/auth/token/create with the given options and
// returns the new token's client_token. Caller must have a token with
// sys/auth/token/create capability (root, by definition, at this stage of
// the Backend bootstrap).
func (c *Client) CreateToken(ctx context.Context, req CreateTokenRequest) (string, error) {
	var resp CreateTokenResponse
	if _, err := c.do(ctx, http.MethodPost, "/v1/auth/token/create", req, &resp); err != nil {
		return "", fmt.Errorf("create token: %w", err)
	}
	if resp.Auth.ClientToken == "" {
		return "", fmt.Errorf("create token: empty client_token in response")
	}
	return resp.Auth.ClientToken, nil
}
