// Package client is the public HTTP client library for talking to a
// running agentchatd daemon over its Unix socket. The agentchat CLI
// consumes it; third-party agent SDKs may also import it.
//
// All methods take a context.Context and return either a typed response
// pointer or an *errcode.Error describing the failure. Network failures
// are mapped to errcode.Unavailable so callers can distinguish them
// from authoritative server-side errors.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	apiv1 "github.com/LinZiyang666/agentchat/internal/api/v1"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Client is a single-daemon HTTP client. Safe for concurrent use.
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

// New returns a Client that dials the given Unix socket on every
// request and authenticates with bearer token. token may be empty if
// the caller only needs unauthenticated endpoints (/v1/healthz).
func New(socketPath, token string) *Client {
	tr := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
		// Keep-alives are fine on a local socket but the CLI is
		// short-lived so it makes little difference.
	}
	return &Client{
		httpClient: &http.Client{Transport: tr, Timeout: 30 * time.Second},
		token:      token,
		// The host part is ignored on a Unix socket; we still need a
		// syntactically valid URL.
		baseURL: "http://agentchatd",
	}
}

// SetTimeout overrides the per-request timeout. Set 0 for no timeout
// (the underlying *http.Client also has its own).
func (c *Client) SetTimeout(d time.Duration) {
	c.httpClient.Timeout = d
}

// do executes one request and decodes the response into target.
// Non-2xx responses are decoded as ErrorEnvelope and returned as an
// *errcode.Error.
func (c *Client) do(ctx context.Context, method, path string, body any, target any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return errcode.Wrap(err, errcode.Internal, "marshal request body")
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "build request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errcode.Wrap(err, errcode.Unavailable, "talk to daemon")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var env apiv1.ErrorEnvelope
		raw, _ := io.ReadAll(resp.Body)
		if jerr := json.Unmarshal(raw, &env); jerr == nil && env.Error.Code != "" {
			return (&errcode.Error{
				Code:    errcode.Code(env.Error.Code),
				Message: env.Error.Message,
			}).WithDetails(env.Error.Details)
		}
		return errcode.New(errcode.Internal,
			"daemon returned HTTP %d: %s", resp.StatusCode, string(raw))
	}

	if target == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return errcode.Wrap(err, errcode.Internal, "decode response")
	}
	return nil
}

// Healthz reports daemon liveness.
func (c *Client) Healthz(ctx context.Context) (*apiv1.HealthzResponse, error) {
	var out apiv1.HealthzResponse
	if err := c.do(ctx, http.MethodGet, "/v1/healthz", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Whoami returns the calling account.
func (c *Client) Whoami(ctx context.Context) (*apiv1.WhoamiResponse, error) {
	var out apiv1.WhoamiResponse
	if err := c.do(ctx, http.MethodGet, "/v1/whoami", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- Accounts ----

func (c *Client) CreateAccount(ctx context.Context, name, role string) (*apiv1.AccountResponse, error) {
	var out apiv1.AccountResponse
	err := c.do(ctx, http.MethodPost, "/v1/accounts",
		apiv1.CreateAccountRequest{Name: name, Role: role}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListAccounts(ctx context.Context) ([]apiv1.AccountResponse, error) {
	var out apiv1.AccountListResponse
	if err := c.do(ctx, http.MethodGet, "/v1/accounts", nil, &out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

func (c *Client) GetAccount(ctx context.Context, id string) (*apiv1.AccountResponse, error) {
	var out apiv1.AccountResponse
	if err := c.do(ctx, http.MethodGet, "/v1/accounts/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RenameAccount(ctx context.Context, id, newName string) (*apiv1.AccountResponse, error) {
	var out apiv1.AccountResponse
	err := c.do(ctx, http.MethodPatch, "/v1/accounts/"+id,
		apiv1.UpdateAccountRequest{Name: &newName}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SetAccountRole(ctx context.Context, id, role string) (*apiv1.AccountResponse, error) {
	var out apiv1.AccountResponse
	err := c.do(ctx, http.MethodPatch, "/v1/accounts/"+id,
		apiv1.UpdateAccountRequest{Role: &role}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteAccount(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/accounts/"+id, nil, nil)
}

// ---- Tokens ----

func (c *Client) CreateToken(ctx context.Context, accountID string) (*apiv1.CreateTokenResponse, error) {
	var out apiv1.CreateTokenResponse
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/accounts/%s/tokens", accountID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListTokens(ctx context.Context, accountID string) ([]apiv1.TokenResponse, error) {
	var out apiv1.TokenListResponse
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/v1/accounts/%s/tokens", accountID), nil, &out)
	if err != nil {
		return nil, err
	}
	return out.Tokens, nil
}

func (c *Client) RevokeToken(ctx context.Context, tokenID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/tokens/"+tokenID, nil, nil)
}

// ---- Audit ----

// ListAuditOptions narrows ListAudit results.
type ListAuditOptions struct {
	AccountID string
	Since     *time.Time
	Limit     int
}

func (c *Client) ListAudit(ctx context.Context, opts ListAuditOptions) ([]apiv1.AuditEntryResponse, error) {
	q := ""
	sep := "?"
	if opts.AccountID != "" {
		q += sep + "account=" + opts.AccountID
		sep = "&"
	}
	if opts.Since != nil {
		q += sep + "since=" + opts.Since.UTC().Format(time.RFC3339)
		sep = "&"
	}
	if opts.Limit > 0 {
		q += sep + "limit=" + fmt.Sprint(opts.Limit)
	}
	var out apiv1.AuditListResponse
	if err := c.do(ctx, http.MethodGet, "/v1/audit"+q, nil, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}
