package errcode

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	err := New(NotFound, "account %s not found", "acc-123")
	require.NotNil(t, err)
	assert.Equal(t, NotFound, err.Code)
	assert.Equal(t, "account acc-123 not found", err.Message)
	assert.Nil(t, err.Details)
	assert.NoError(t, err.Unwrap())
}

func TestWrap(t *testing.T) {
	t.Run("nil cause returns nil", func(t *testing.T) {
		assert.Nil(t, Wrap(nil, Internal, "x"))
	})
	t.Run("plain error gets wrapped", func(t *testing.T) {
		cause := errors.New("io failed")
		err := Wrap(cause, Internal, "while doing X")
		require.NotNil(t, err)
		assert.Equal(t, Internal, err.Code)
		assert.Equal(t, "while doing X", err.Message)
		assert.ErrorIs(t, err, cause)
	})
	t.Run("same-code Error is returned unchanged (idempotent)", func(t *testing.T) {
		base := New(NotFound, "x")
		again := Wrap(base, NotFound, "wrapped")
		assert.Same(t, base, again, "Wrap should not double-wrap when code matches")
	})
	t.Run("different-code Error gets wrapped", func(t *testing.T) {
		inner := New(NotFound, "inner")
		outer := Wrap(inner, Internal, "outer")
		require.NotNil(t, outer)
		assert.Equal(t, Internal, outer.Code)
		assert.Equal(t, inner, errors.Unwrap(outer))
	})
}

func TestIs(t *testing.T) {
	a := New(NotFound, "x")
	b := New(NotFound, "y") // same code, different message
	c := New(Conflict, "x") // different code

	assert.True(t, errors.Is(a, b), "Errors with same code should be Is-equal")
	assert.False(t, errors.Is(a, c), "Errors with different code should not be Is-equal")
	assert.False(t, errors.Is(a, errors.New("plain")), "Non-Error target should not match")
}

func TestAs(t *testing.T) {
	plain := errors.New("plain")
	_, ok := As(plain)
	assert.False(t, ok)

	custom := New(InvalidArgument, "bad")
	got, ok := As(fmt.Errorf("wrapped: %w", custom))
	require.True(t, ok)
	assert.Equal(t, InvalidArgument, got.Code)
}

func TestWithDetails(t *testing.T) {
	base := New(Conflict, "name taken")
	withName := base.WithDetails(map[string]any{"name": "agent1"})
	require.NotNil(t, withName)
	assert.Nil(t, base.Details, "WithDetails must not mutate receiver")
	assert.Equal(t, "agent1", withName.Details["name"])

	withMore := withName.WithDetails(map[string]any{"reason": "duplicate"})
	assert.Equal(t, "agent1", withMore.Details["name"], "earlier details must be preserved")
	assert.Equal(t, "duplicate", withMore.Details["reason"])
}

func TestErrorOnNil(t *testing.T) {
	var e *Error
	assert.Equal(t, "", e.Error())
	assert.NoError(t, e.Unwrap())
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain", errors.New("oops"), 1},
		{"missing", New(AuthMissing, ""), 10},
		{"invalid", New(AuthInvalid, ""), 11},
		{"revoked", New(AuthRevoked, ""), 12},
		{"perm", New(PermDenied, ""), 13},
		{"notfound", New(NotFound, ""), 20},
		{"conflict", New(Conflict, ""), 21},
		{"badarg", New(InvalidArgument, ""), 22},
		{"internal", New(Internal, ""), 50},
		{"unavail", New(Unavailable, ""), 51},
		{"unspecified", New(Unspecified, ""), 1},
		{"wrapped", fmt.Errorf("outer: %w", New(NotFound, "")), 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, ExitCode(c.err))
		})
	}
}

func TestHTTPStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, http.StatusOK},
		{"plain", errors.New("oops"), http.StatusInternalServerError},
		{"missing", New(AuthMissing, ""), http.StatusUnauthorized},
		{"invalid", New(AuthInvalid, ""), http.StatusUnauthorized},
		{"revoked", New(AuthRevoked, ""), http.StatusUnauthorized},
		{"perm", New(PermDenied, ""), http.StatusForbidden},
		{"notfound", New(NotFound, ""), http.StatusNotFound},
		{"conflict", New(Conflict, ""), http.StatusConflict},
		{"badarg", New(InvalidArgument, ""), http.StatusBadRequest},
		{"unavail", New(Unavailable, ""), http.StatusServiceUnavailable},
		{"internal", New(Internal, ""), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, HTTPStatus(c.err))
		})
	}
}
