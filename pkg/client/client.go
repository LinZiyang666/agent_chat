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

// ---- M3: Discord + lifecycle ----

// SetDiscord encrypts and stores a bot token on the given account.
func (c *Client) SetDiscord(ctx context.Context, accountID, botToken string) (*apiv1.AccountResponse, error) {
	var out apiv1.AccountResponse
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/accounts/%s/discord", accountID),
		apiv1.SetDiscordRequest{BotToken: botToken}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Online brings the account's Discord Provider online.
func (c *Client) Online(ctx context.Context, accountID string) (*apiv1.StatusResponse, error) {
	var out apiv1.StatusResponse
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/accounts/%s/online", accountID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Offline disconnects the account's Provider.
func (c *Client) Offline(ctx context.Context, accountID string) (*apiv1.StatusResponse, error) {
	var out apiv1.StatusResponse
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/accounts/%s/offline", accountID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Status returns the current lifecycle + Provider status of accountID.
func (c *Client) Status(ctx context.Context, accountID string) (*apiv1.StatusResponse, error) {
	var out apiv1.StatusResponse
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/v1/accounts/%s/status", accountID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DebugSend asks the daemon to send a message via the account's live
// Provider. M3 diagnostic; the M4 rooms subsystem will replace it.
func (c *Client) DebugSend(ctx context.Context, accountID, channelID, content string) (*apiv1.DebugSendResponse, error) {
	var out apiv1.DebugSendResponse
	err := c.do(ctx, http.MethodPost, "/v1/debug/send", apiv1.DebugSendRequest{
		AccountID: accountID,
		ChannelID: channelID,
		Content:   content,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamEvents opens an NDJSON event stream for accountID. Each line
// in the response body decodes into an Event. The channel is closed
// when ctx is cancelled or the daemon closes the connection.
//
// Caller responsibilities:
//   - call cancel on the supplied context to stop the stream early;
//   - drain the channel until it is closed.
func (c *Client) StreamEvents(ctx context.Context, accountID string) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v1/debug/events?account="+accountID, nil)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "build request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// Streaming endpoint: disable any per-request timeout for this
	// call. The transport-level dial timeout still applies via the
	// underlying RoundTripper.
	streamClient := &http.Client{Transport: c.httpClient.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Unavailable, "open event stream")
	}
	if resp.StatusCode >= 400 {
		var env apiv1.ErrorEnvelope
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if jerr := json.Unmarshal(raw, &env); jerr == nil && env.Error.Code != "" {
			return nil, (&errcode.Error{
				Code:    errcode.Code(env.Error.Code),
				Message: env.Error.Message,
			}).WithDetails(env.Error.Details)
		}
		return nil, errcode.New(errcode.Internal,
			"daemon returned HTTP %d on stream open: %s", resp.StatusCode, string(raw))
	}

	out := make(chan Event, 32)
	go streamEvents(resp, out)
	return out, nil
}

// streamEvents reads NDJSON from resp.Body and pushes onto out.
// Closes the channel on EOF, decode error, or context cancellation.
func streamEvents(resp *http.Response, out chan<- Event) {
	defer close(out)
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			return
		}
		out <- ev
	}
}

// Event is the pkg/client view of a daemon event (mirrors the daemon's
// NDJSON envelope shape).
type Event struct {
	Type     string      `json:"type"`
	Message  interface{} `json:"message,omitempty"`
	Identity interface{} `json:"identity,omitempty"`
	Reason   string      `json:"reason,omitempty"`
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

// M4 — rooms.

func (c *Client) CreateRoom(ctx context.Context, name string) (*apiv1.RoomResponse, error) {
	var out apiv1.RoomResponse
	if err := c.do(ctx, http.MethodPost, "/v1/rooms",
		apiv1.CreateRoomRequest{Name: name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRoomsOptions narrows ListRooms results.
type ListRoomsOptions struct {
	IncludeArchived bool
}

func (c *Client) ListRooms(ctx context.Context, opts ListRoomsOptions) ([]apiv1.RoomResponse, error) {
	q := ""
	if opts.IncludeArchived {
		q = "?include_archived=true"
	}
	var out apiv1.RoomListResponse
	if err := c.do(ctx, http.MethodGet, "/v1/rooms"+q, nil, &out); err != nil {
		return nil, err
	}
	return out.Rooms, nil
}

func (c *Client) GetRoom(ctx context.Context, id string) (*apiv1.RoomResponse, error) {
	var out apiv1.RoomResponse
	if err := c.do(ctx, http.MethodGet, "/v1/rooms/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RenameRoom(ctx context.Context, id, name string) (*apiv1.RoomResponse, error) {
	var out apiv1.RoomResponse
	if err := c.do(ctx, http.MethodPatch, "/v1/rooms/"+id,
		apiv1.UpdateRoomRequest{Name: &name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ArchiveRoom(ctx context.Context, id string) (*apiv1.RoomResponse, error) {
	var out apiv1.RoomResponse
	if err := c.do(ctx, http.MethodPost, "/v1/rooms/"+id+"/archive", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteRoom(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/rooms/"+id, nil, nil)
}

func (c *Client) InviteMember(ctx context.Context, roomID, accountID string, subscribed bool) (*apiv1.MembershipResponse, error) {
	var out apiv1.MembershipResponse
	if err := c.do(ctx, http.MethodPost, "/v1/rooms/"+roomID+"/members",
		apiv1.InviteRequest{AccountID: accountID, Subscribed: subscribed}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) KickMember(ctx context.Context, roomID, accountID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/rooms/"+roomID+"/members/"+accountID, nil, nil)
}

func (c *Client) ListMembers(ctx context.Context, roomID string) ([]apiv1.MembershipResponse, error) {
	var out apiv1.MembershipListResponse
	if err := c.do(ctx, http.MethodGet, "/v1/rooms/"+roomID+"/members", nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

func (c *Client) SetSubscribed(ctx context.Context, roomID string, subscribed bool) (*apiv1.MembershipResponse, error) {
	var out apiv1.MembershipResponse
	if err := c.do(ctx, http.MethodPatch, "/v1/memberships/"+roomID,
		apiv1.UpdateMembershipRequest{Subscribed: subscribed}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMessageOptions wraps the optional bits of SendMessage.
type SendMessageOptions struct {
	ReplyToID   string
	RequiresAck bool
	Priority    string
}

func (c *Client) SendMessage(ctx context.Context, roomID, content string, opts SendMessageOptions) (*apiv1.MessageResponse, error) {
	var out apiv1.MessageResponse
	if err := c.do(ctx, http.MethodPost, "/v1/rooms/"+roomID+"/messages",
		apiv1.SendMessageRequest{
			Content:     content,
			ReplyToID:   opts.ReplyToID,
			RequiresAck: opts.RequiresAck,
			Priority:    opts.Priority,
		}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMessagesOptions narrows ListMessages results.
type ListMessagesOptions struct {
	Before string
	Limit  int
}

func (c *Client) ListMessages(ctx context.Context, roomID string, opts ListMessagesOptions) ([]apiv1.MessageResponse, error) {
	q := ""
	sep := "?"
	if opts.Before != "" {
		q += sep + "before=" + opts.Before
		sep = "&"
	}
	if opts.Limit > 0 {
		q += sep + "limit=" + fmt.Sprint(opts.Limit)
	}
	var out apiv1.MessageListResponse
	if err := c.do(ctx, http.MethodGet, "/v1/rooms/"+roomID+"/messages"+q, nil, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Client) MarkMessageRead(ctx context.Context, messageID string) (*apiv1.MessageStateResponse, error) {
	var out apiv1.MessageStateResponse
	if err := c.do(ctx, http.MethodPost, "/v1/messages/"+messageID+"/read", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ReplyAckMessage(ctx context.Context, messageID string) (*apiv1.MessageStateResponse, error) {
	var out apiv1.MessageStateResponse
	if err := c.do(ctx, http.MethodPost, "/v1/messages/"+messageID+"/reply-ack", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
