package update

import "fmt"

type ErrorKind string

func (k ErrorKind) Error() string { return string(k) }

const (
	ErrInvalidRequest        ErrorKind = "invalid_request"
	ErrInvalidReceipt        ErrorKind = "invalid_install_receipt"
	ErrReceiptMismatch       ErrorKind = "install_receipt_mismatch"
	ErrUnsupportedOwner      ErrorKind = "unsupported_install_owner"
	ErrReleaseNotFound       ErrorKind = "release_not_found"
	ErrReleaseResolution     ErrorKind = "release_resolution_failed"
	ErrUpdateInProgress      ErrorKind = "update_in_progress"
	ErrChecksumMismatch      ErrorKind = "checksum_mismatch"
	ErrSignatureVerification ErrorKind = "signature_verification_failed"
	ErrApplyFailed           ErrorKind = "update_apply_failed"
	ErrRollbackUnavailable   ErrorKind = "rollback_unavailable"
	ErrRollbackFailed        ErrorKind = "rollback_failed"
)

type Error struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
	Owner   Owner     `json:"owner,omitempty"`
	Command []string  `json:"action_required,omitempty"`
	Cause   error     `json:"-"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) Is(target error) bool {
	kind, ok := target.(ErrorKind)
	return ok && e.Kind == kind
}

func newError(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}
