package errcode

// ExitCode returns the POSIX exit code the CLI should use when surfacing
// the given error to the shell. Stable contract: agents and scripts may
// branch on these values.
//
//	 0   success (callers don't pass errors here)
//	 1   generic / unclassified failure
//	 2   CLI-side argument error (cobra reserves this; matches convention)
//	10   AUTH_MISSING
//	11   AUTH_INVALID
//	12   AUTH_REVOKED
//	13   PERM_DENIED
//	20   NOT_FOUND
//	21   CONFLICT
//	22   INVALID_ARGUMENT  (also ATTACHMENT_TOO_LARGE, PAYLOAD_TOO_LARGE — same
//	     family; callers wanting to distinguish should branch on the JSON `code` field)
//	50   INTERNAL
//	51   UNAVAILABLE
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	e, ok := As(err)
	if !ok {
		return 1
	}
	switch e.Code {
	case AuthMissing:
		return 10
	case AuthInvalid:
		return 11
	case AuthRevoked:
		return 12
	case PermDenied:
		return 13
	case NotFound:
		return 20
	case Conflict:
		return 21
	case InvalidArgument, AttachmentTooLarge, PayloadTooLarge:
		return 22
	case ResourceExhausted:
		// Same family as Conflict (current state rejects the request).
		return 21
	case Internal:
		return 50
	case Unavailable:
		return 51
	default:
		return 1
	}
}
