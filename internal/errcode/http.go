package errcode

import "net/http"

// HTTPStatus returns the HTTP response status for an Error. Used by API
// handlers to render structured error bodies.
func HTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	e, ok := As(err)
	if !ok {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case AuthMissing, AuthInvalid, AuthRevoked:
		return http.StatusUnauthorized
	case PermDenied:
		return http.StatusForbidden
	case NotFound:
		return http.StatusNotFound
	case Conflict:
		return http.StatusConflict
	case InvalidArgument:
		return http.StatusBadRequest
	case AttachmentTooLarge, PayloadTooLarge:
		// Both 413: AttachmentTooLarge is about the Discord upload
		// cap; PayloadTooLarge is about the JSON request body cap.
		return http.StatusRequestEntityTooLarge
	case ResourceExhausted:
		return http.StatusTooManyRequests
	case Unavailable:
		return http.StatusServiceUnavailable
	case Internal, Unspecified:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
