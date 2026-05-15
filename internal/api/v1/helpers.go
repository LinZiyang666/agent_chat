package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// WriteJSON serializes body as JSON, sets Content-Type, and writes
// status. It silently swallows the encode error because by the time
// Encode fails the response status has been written.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// WriteError converts err into a structured ErrorEnvelope and writes it
// with the HTTP status mapped by errcode.HTTPStatus. Callers can use
// any error type; non-errcode errors collapse to INTERNAL.
func WriteError(w http.ResponseWriter, err error) {
	if err == nil {
		WriteJSON(w, http.StatusOK, nil)
		return
	}
	var (
		body   ErrorBody
		status = errcode.HTTPStatus(err)
	)
	if e, ok := errcode.As(err); ok {
		body = ErrorBody{
			Code:    string(e.Code),
			Message: e.Message,
			Details: e.Details,
		}
	} else {
		body = ErrorBody{
			Code:    string(errcode.Internal),
			Message: err.Error(),
		}
	}
	WriteJSON(w, status, ErrorEnvelope{Error: body})
}

// DecodeJSON reads r.Body as JSON into target. Unknown fields trigger
// an InvalidArgument error so typos are caught early.
func DecodeJSON(r *http.Request, target any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		// Distinguish "empty body" (EOF) from "malformed JSON".
		if errors.Is(err, errors.New("EOF")) || err.Error() == "EOF" {
			return errcode.New(errcode.InvalidArgument, "empty request body")
		}
		return errcode.Wrap(err, errcode.InvalidArgument, "decode request body")
	}
	return nil
}

// AccountToResponse converts a store.Account into its public DTO.
func AccountToResponse(a *store.Account) AccountResponse {
	if a == nil {
		return AccountResponse{}
	}
	return AccountResponse{
		ID:             a.ID,
		Name:           a.Name,
		Role:           string(a.Role),
		LifecycleState: string(a.LifecycleState),
		CreatedAt:      a.CreatedAt,
		UpdatedAt:      a.UpdatedAt,
	}
}

// TokenToResponse converts a store.Token into its public DTO. The hash
// is never serialized.
func TokenToResponse(t *store.Token) TokenResponse {
	if t == nil {
		return TokenResponse{}
	}
	resp := TokenResponse{
		ID:        t.ID,
		AccountID: t.AccountID,
		CreatedAt: t.CreatedAt,
	}
	if t.RevokedAt != nil {
		v := t.RevokedAt.UTC()
		resp.RevokedAt = &v
	}
	if t.LastUsedAt != nil {
		v := t.LastUsedAt.UTC()
		resp.LastUsedAt = &v
	}
	return resp
}

// AuditToResponse converts a store.AuditEntry into its public DTO.
func AuditToResponse(e *store.AuditEntry) AuditEntryResponse {
	if e == nil {
		return AuditEntryResponse{}
	}
	return AuditEntryResponse{
		ID:        e.ID,
		AccountID: e.AccountID,
		Action:    e.Action,
		Target:    e.Target,
		Payload:   e.Payload,
		CreatedAt: e.CreatedAt,
	}
}

// RoomToResponse converts a store.Room into its public DTO.
func RoomToResponse(r *store.Room) RoomResponse {
	if r == nil {
		return RoomResponse{}
	}
	return RoomResponse{
		ID:               r.ID,
		DiscordChannelID: r.DiscordChannelID,
		Name:             r.Name,
		Archived:         r.Archived,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

// MembershipToResponse converts a store.Membership into its public DTO.
func MembershipToResponse(m *store.Membership) MembershipResponse {
	if m == nil {
		return MembershipResponse{}
	}
	return MembershipResponse{
		AccountID:  m.AccountID,
		RoomID:     m.RoomID,
		Subscribed: m.Subscribed,
		JoinedAt:   m.JoinedAt,
	}
}

// MessageToResponse converts a store.Message into its public DTO.
func MessageToResponse(m *store.Message) MessageResponse {
	if m == nil {
		return MessageResponse{}
	}
	return MessageResponse{
		ID:              m.ID,
		RoomID:          m.RoomID,
		AuthorAccountID: m.AuthorAccountID,
		DiscordMsgID:    m.DiscordMsgID,
		Content:         m.Content,
		ReplyToMsgID:    m.ReplyToMsgID,
		RequiresAck:     m.RequiresAck,
		Priority:        string(m.Priority),
		CreatedAt:       m.CreatedAt,
		ContentHash:     m.ContentHash,
		MentionAll:      m.MentionAll,
	}
}

// AnnouncementToResponse converts a store.Announcement into its public DTO.
// Read is left at the zero value; callers set it after consulting the
// read repo.
func AnnouncementToResponse(a *store.Announcement) AnnouncementResponse {
	if a == nil {
		return AnnouncementResponse{}
	}
	return AnnouncementResponse{
		ID:        a.ID,
		RoomID:    a.RoomID,
		Content:   a.Content,
		Version:   a.Version,
		CreatedBy: a.CreatedBy,
		CreatedAt: a.CreatedAt,
	}
}

// SystemAnnouncementToResponse converts a store.SystemAnnouncement to its DTO.
func SystemAnnouncementToResponse(a *store.SystemAnnouncement) SystemAnnouncementResponse {
	if a == nil {
		return SystemAnnouncementResponse{}
	}
	return SystemAnnouncementResponse{
		ID:        a.ID,
		Content:   a.Content,
		CreatedBy: a.CreatedBy,
		CreatedAt: a.CreatedAt,
	}
}

// AttachmentToResponse converts a store.Attachment into its DTO (M7).
func AttachmentToResponse(a *store.Attachment) AttachmentResponse {
	if a == nil {
		return AttachmentResponse{}
	}
	out := AttachmentResponse{
		ID:         a.ID,
		MessageID:  a.MessageID,
		Filename:   a.Filename,
		Size:       a.Size,
		MIME:       a.MIME,
		LocalPath:  a.LocalPath,
		DiscordURL: a.DiscordURL,
		SHA256:     a.SHA256,
	}
	if a.DownloadedAt != nil {
		v := a.DownloadedAt.UTC()
		out.DownloadedAt = &v
	}
	return out
}

// MessageStateToResponse converts a store.MessageState into its public DTO.
func MessageStateToResponse(s *store.MessageState) MessageStateResponse {
	if s == nil {
		return MessageStateResponse{}
	}
	out := MessageStateResponse{
		MessageID: s.MessageID,
		AccountID: s.AccountID,
	}
	if s.ReadAt != nil {
		v := s.ReadAt.UTC()
		out.ReadAt = &v
	}
	if s.RepliedAt != nil {
		v := s.RepliedAt.UTC()
		out.RepliedAt = &v
	}
	return out
}

// ParseSinceParam interprets the ?since= query parameter as an RFC3339
// timestamp. Returns nil for missing/empty values.
func ParseSinceParam(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.InvalidArgument, "parse 'since' as RFC3339")
	}
	return &t, nil
}

// AuditOrFail records an audit entry and writes a 500 response on
// failure. **Deprecated** as of the M2-P3-012 fix: every mutating
// handler now uses store.Bundler.WithTx + Recorder.RecordVia so the
// mutation and the audit insert share a transaction and roll back
// together. AuditOrFail is retained only for non-mutating bookkeeping
// (none today) and remains here so older call sites can be located
// during review.
func AuditOrFail(w http.ResponseWriter, r *http.Request, rec *audit.Recorder, action audit.Action, target string, payload any) bool {
	actor, ok := auth.AccountFromContext(r.Context())
	if !ok {
		WriteError(w, errcode.New(errcode.Internal, "audit called without authenticated actor"))
		return false
	}
	if err := rec.Record(r.Context(), actor.ID, action, target, payload); err != nil {
		WriteError(w, errcode.Wrap(err, errcode.Internal, "record audit"))
		return false
	}
	return true
}
