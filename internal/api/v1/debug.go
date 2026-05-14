package v1

import (
	"encoding/json"
	"net/http"

	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// DebugSendRequest is the body of POST /v1/debug/send.
type DebugSendRequest struct {
	AccountID string `json:"account_id"`
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
}

// DebugSendResponse is the body returned on success.
type DebugSendResponse struct {
	Message bot.Message `json:"message"`
}

// DebugSend is a thin diagnostic endpoint that drives a live Provider
// to send a message into a channel. It exists for the M3 demo and
// future operator debugging; M4 will replace it with the rooms
// subsystem.
//
// Admin-only (enforced by the router); no transactional audit yet
// because the action is read-mostly diagnostic — operators can still
// see the trace in daemon logs.
func DebugSend(conn *connector.Connector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req DebugSendRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if req.AccountID == "" || req.ChannelID == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "account_id and channel_id are required"))
			return
		}
		p, ok := conn.Provider(req.AccountID)
		if !ok {
			WriteError(w, errcode.New(errcode.Conflict, "account %s is not online", req.AccountID))
			return
		}
		msg, err := p.SendMessage(r.Context(), req.ChannelID, req.Content, bot.SendOptions{})
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, DebugSendResponse{Message: *msg})
	}
}

// DebugEvents is GET /v1/debug/events?account=<id>. It streams events
// from the named account's Provider as NDJSON (one JSON object per
// line, no padding, no heartbeat).
//
// Connection closes when the client goes away (request context done)
// or when the underlying Provider disconnects.
func DebugEvents(conn *connector.Connector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := r.URL.Query().Get("account")
		if accountID == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "account query parameter is required"))
			return
		}
		if _, ok := conn.Provider(accountID); !ok {
			WriteError(w, errcode.New(errcode.Conflict, "account %s is not online", accountID))
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "response writer does not support flushing"))
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		sub := conn.Subscribe(accountID)
		defer conn.Unsubscribe(sub)

		enc := json.NewEncoder(w)
		for {
			select {
			case ev, ok := <-sub.C:
				if !ok {
					return
				}
				wire := wireEvent(ev)
				if err := enc.Encode(wire); err != nil {
					return
				}
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

// wireEvent renders a normalized JSON shape for an Event. The type
// discriminator lives in the "type" field; the rest are event-specific
// fields. Agents can parse this without knowing Go types.
type eventEnvelope struct {
	Type     string        `json:"type"`
	Message  *bot.Message  `json:"message,omitempty"`
	Identity *bot.Identity `json:"identity,omitempty"`
	Reason   string        `json:"reason,omitempty"`
}

func wireEvent(ev bot.Event) eventEnvelope {
	switch e := ev.(type) {
	case bot.EventConnected:
		id := e.Identity
		return eventEnvelope{Type: "connected", Identity: &id}
	case bot.EventDisconnected:
		return eventEnvelope{Type: "disconnected", Reason: e.Reason}
	case bot.EventMessageNew:
		m := e.Message
		return eventEnvelope{Type: "message_new", Message: &m}
	default:
		return eventEnvelope{Type: "unknown"}
	}
}
