// Package errcode defines the structured error code enum used in API
// responses and CLI exit-code mapping. Every user-visible error (from the
// daemon or the CLI) carries a Code so agents can branch on it
// deterministically without parsing message strings.
package errcode

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error identifier. New codes may be
// added; existing codes must not change meaning.
type Code string

// Known error codes. Exit codes for the CLI are mapped in exitcode.go;
// HTTP statuses in http.go. The numeric value here is irrelevant — only
// the string is the contract.
const (
	// Unspecified is the zero value; treat as "no code attached".
	Unspecified Code = ""

	// AuthMissing means no credentials were presented.
	AuthMissing Code = "AUTH_MISSING"
	// AuthInvalid means credentials were presented but did not verify.
	AuthInvalid Code = "AUTH_INVALID"
	// AuthRevoked means credentials were once valid but have been revoked.
	AuthRevoked Code = "AUTH_REVOKED"
	// PermDenied means the caller is authenticated but lacks the required
	// role for the operation (e.g. a user trying an admin-only endpoint).
	PermDenied Code = "PERM_DENIED"

	// NotFound means the target resource does not exist.
	NotFound Code = "NOT_FOUND"
	// Conflict means the operation conflicts with current state
	// (e.g. creating an account whose name is taken).
	Conflict Code = "CONFLICT"
	// InvalidArgument means a request field failed validation.
	InvalidArgument Code = "INVALID_ARGUMENT"

	// Internal means an unexpected server-side condition. Indicates a bug
	// or external system failure the caller cannot remediate.
	Internal Code = "INTERNAL"
	// Unavailable means a transient condition: try again later.
	Unavailable Code = "UNAVAILABLE"

	// AttachmentTooLarge (M7) means an outbound attachment exceeded
	// the Discord 25 MB per-message cap. The CLI maps this to its
	// own exit code (22) so scripts can branch on it directly.
	AttachmentTooLarge Code = "ATTACHMENT_TOO_LARGE"
)

// Error is the canonical error type produced by this codebase. It is JSON
// serializable for HTTP responses and unwrappable for Go's errors.Is/As.
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`

	// cause is the wrapped underlying error. Not serialized; surfaced via
	// Unwrap() so callers can use errors.Is / errors.As on it.
	cause error
}

// Error returns the human-readable message. It does not include the code
// because consumers should display Code separately (or rely on JSON).
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap returns the wrapped cause, enabling errors.Is / errors.As across
// the wrapping boundary.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is allows errors.Is(err, errcode.NotFound) style comparisons by matching
// on the Code field of another *Error.
func (e *Error) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// New constructs an Error with the given code and message. Use Wrap to
// attach a cause.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap returns an Error that carries cause as its underlying error. If
// cause is already an *Error with the same code, it is returned unchanged
// so we don't double-wrap.
func Wrap(cause error, code Code, format string, args ...any) *Error {
	if cause == nil {
		return nil
	}
	if existing, ok := cause.(*Error); ok && existing.Code == code {
		return existing
	}
	return &Error{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		cause:   cause,
	}
}

// WithDetails returns a copy of e with the given key/value attached to
// its Details map. The receiver is not mutated.
func (e *Error) WithDetails(kvs map[string]any) *Error {
	if e == nil {
		return nil
	}
	cp := *e
	if cp.Details == nil {
		cp.Details = make(map[string]any, len(kvs))
	} else {
		merged := make(map[string]any, len(cp.Details)+len(kvs))
		for k, v := range cp.Details {
			merged[k] = v
		}
		cp.Details = merged
	}
	for k, v := range kvs {
		cp.Details[k] = v
	}
	return &cp
}

// As is a convenience around errors.As that returns the underlying
// *Error (or nil + false) for an arbitrary error.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
