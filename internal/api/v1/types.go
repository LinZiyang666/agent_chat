// Package v1 holds the HTTP request/response DTOs and handler
// functions for the agentchatd API at /v1/...
//
// Handlers depend on service interfaces from sibling business packages
// (account.Service, audit.Recorder, auth.Manager, store.* repos); they
// must not touch SQL or Discord directly.
package v1

import "time"

// ErrorBody is the inner body of every error response.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorEnvelope wraps ErrorBody so the JSON shape is { "error": {...} }.
// This matches the convention agreed in docs/03-architecture.md §6.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// CreateAccountRequest is the body of POST /v1/accounts.
type CreateAccountRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// UpdateAccountRequest is the body of PATCH /v1/accounts/{id}. All
// fields are optional; omitted fields are left unchanged.
type UpdateAccountRequest struct {
	Name *string `json:"name,omitempty"`
	Role *string `json:"role,omitempty"`
}

// AccountResponse is the public shape of an account.
type AccountResponse struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	LifecycleState string    `json:"lifecycle_state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AccountListResponse is the body of GET /v1/accounts.
type AccountListResponse struct {
	Accounts []AccountResponse `json:"accounts"`
}

// TokenResponse is the public shape of a token, without the secret.
type TokenResponse struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// CreateTokenResponse is the body of POST /v1/accounts/{id}/tokens.
// Raw contains the only copy of the plaintext token the server will
// ever emit; the caller must persist it on the spot.
type CreateTokenResponse struct {
	Token TokenResponse `json:"token"`
	Raw   string        `json:"raw"`
}

// TokenListResponse is the body of GET /v1/accounts/{id}/tokens.
type TokenListResponse struct {
	Tokens []TokenResponse `json:"tokens"`
}

// WhoamiResponse is the body of GET /v1/whoami.
type WhoamiResponse struct {
	Account AccountResponse `json:"account"`
	TokenID string          `json:"token_id"`
}

// AuditEntryResponse is the public shape of an audit row.
type AuditEntryResponse struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Action    string    `json:"action"`
	Target    string    `json:"target,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditListResponse is the body of GET /v1/audit.
type AuditListResponse struct {
	Entries []AuditEntryResponse `json:"entries"`
}
