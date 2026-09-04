package domain

import "errors"

var (
	ErrDeviceNotExposed       = errors.New("device is not exposed")
	ErrDeviceIdentityMismatch = errors.New("device identity does not match pinned identity")
	ErrCapabilityUnavailable  = errors.New("capability is unavailable")
	ErrFirmwareUnsupported    = errors.New("firmware is unsupported")
	ErrTakeoverDisabled       = errors.New("WebRTC takeover is disabled")
)
