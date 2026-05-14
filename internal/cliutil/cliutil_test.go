package cliutil

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

func TestPrintErrorWithCode(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, errcode.New(errcode.NotFound, "no such account"))
	got := buf.String()
	assert.Contains(t, got, "[NOT_FOUND]")
	assert.Contains(t, got, "no such account")
}

func TestPrintErrorPlain(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, errors.New("kaboom"))
	got := buf.String()
	assert.Contains(t, got, "kaboom")
	assert.NotContains(t, got, "[")
}

func TestPrintErrorWithDetails(t *testing.T) {
	var buf bytes.Buffer
	e := errcode.New(errcode.Conflict, "name taken").
		WithDetails(map[string]any{"name": "alice"})
	PrintError(&buf, e)
	got := buf.String()
	assert.Contains(t, got, "Details:")
	assert.Contains(t, got, "name: alice")
}

func TestPrintErrorNil(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, nil)
	assert.Empty(t, buf.String())
}

// TestPrintErrorWithCause locks the M2-P3-011 fix: when an
// *errcode.Error wraps another error, the wrapped cause must be
// surfaced on a "Caused by:" line.
func TestPrintErrorWithCause(t *testing.T) {
	var buf bytes.Buffer
	cause := errors.New("operation not permitted")
	wrapped := errcode.Wrap(cause, errcode.Internal, "bind socket")
	PrintError(&buf, wrapped)
	got := buf.String()
	assert.Contains(t, got, "[INTERNAL]")
	assert.Contains(t, got, "bind socket")
	assert.Contains(t, got, "Caused by:")
	assert.Contains(t, got, "operation not permitted")
}

// TestPrintErrorNoCauseNoCausedByLine confirms that errors without a
// wrapped cause do not emit a misleading "Caused by:" blank line.
func TestPrintErrorNoCauseNoCausedByLine(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, errcode.New(errcode.NotFound, "x"))
	assert.NotContains(t, buf.String(), "Caused by:")
}

func TestIsTerminalNil(t *testing.T) {
	assert.False(t, IsTerminal(nil))
}
