package update

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReleaseValidatorChecksChecksumThenSignature(t *testing.T) {
	payload := []byte("archive")
	hash := sha256.Sum256(payload)
	checksums := []byte(fmt.Sprintf("%x  jetkvm_linux_amd64.tar.gz\n", hash))
	signature := &recordingSignatureVerifier{}
	validator := newReleaseValidator(signature).(*releaseValidator)

	if name := validator.GetValidationAssetName("jetkvm_linux_amd64.tar.gz"); name != "checksums.txt" {
		t.Fatalf("checksum asset = %q", name)
	}
	if err := validator.Validate("jetkvm_linux_amd64.tar.gz", payload, checksums); err != nil {
		t.Fatal(err)
	}
	if !validator.MustContinueValidation("checksums.txt") {
		t.Fatal("signature validation chain did not continue")
	}
	if name := validator.GetValidationAssetName("checksums.txt"); name != "checksums.txt.sigstore.json" {
		t.Fatalf("signature asset = %q", name)
	}
	if err := validator.Validate("checksums.txt", checksums, []byte("bundle")); err != nil {
		t.Fatal(err)
	}
	if !signature.called {
		t.Fatal("signature verifier was not called")
	}
}

func TestReleaseValidatorClassifiesFailures(t *testing.T) {
	validator := newReleaseValidator(signatureVerifierFunc(func([]byte, []byte) error {
		return fmt.Errorf("bad signature")
	})).(*releaseValidator)
	if kind := kindOf(validator.Validate("asset.tar.gz", []byte("bad"), []byte("invalid"))); kind != ErrChecksumMismatch {
		t.Fatalf("checksum kind = %q", kind)
	}
	if kind := kindOf(validator.Validate("checksums.txt", []byte("sum"), []byte("bundle"))); kind != ErrSignatureVerification {
		t.Fatalf("signature kind = %q", kind)
	}
}

func TestGitHubBackendRequiresSignatureVerifier(t *testing.T) {
	_, err := NewGitHubBackend(GitHubBackendConfig{})
	if kindOf(err) != ErrSignatureVerification {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrSignatureVerification)
	}
}

func TestSelfCheckCandidateVerifiesReleasedBinaryIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture is a POSIX script")
	}
	path := filepath.Join(t.TempDir(), "jetkvm")
	fixture := `#!/bin/sh
printf '%s\n' '{"schema_version":"v1","command":"version","data":{"version":"1.2.3","commit":"abc123","date":"2026-09-05T00:00:00Z","go":"go1.27.0","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `"}}'
`
	if err := os.WriteFile(path, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := selfCheckCandidate(t.Context(), path, "1.2.3", runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	if err := selfCheckCandidate(t.Context(), path, "1.2.4", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("self-check accepted a mismatched release version")
	}
}

type recordingSignatureVerifier struct{ called bool }

func (v *recordingSignatureVerifier) Verify(_, _ []byte) error {
	v.called = true
	return nil
}

type signatureVerifierFunc func([]byte, []byte) error

func (f signatureVerifierFunc) Verify(payload, bundle []byte) error { return f(payload, bundle) }
