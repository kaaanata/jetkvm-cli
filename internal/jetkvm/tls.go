package jetkvm

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net/http"
)

var ErrInvalidSPKIPin = errors.New("invalid SPKI SHA-256 pin")

// NewPinnedHTTPClient constructs an HTTP client whose TLS authority is the
// configured leaf-certificate SPKI digest. It never falls back to system trust
// or plain HTTP when the pin does not match.
func NewPinnedHTTPClient(pinHex string) (*http.Client, error) {
	pin, err := hex.DecodeString(pinHex)
	if err != nil || len(pin) != sha256.Size {
		return nil, ErrInvalidSPKIPin
	}
	expected := [sha256.Size]byte(pin)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Pin verification below is the configured TLS authority. Keeping the
		// default verifier enabled would reject intentionally pinned self-signed
		// JetKVM certificates before the SPKI authority can be evaluated.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrInvalidSPKIPin
			}
			observed := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(observed[:], expected[:]) != 1 {
				return ErrInvalidSPKIPin
			}
			return nil
		},
	}
	return &http.Client{Transport: transport}, nil
}
