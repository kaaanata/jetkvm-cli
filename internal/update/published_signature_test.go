package update

import (
	"os"
	"testing"
)

// Release signing invokes this test before GoReleaser uploads the artifacts.
// It verifies exactly the same bundle and trust policy as installed clients.
func TestPublishedReleaseSignature(t *testing.T) {
	checksumPath := os.Getenv("JETKVM_TEST_CHECKSUM_PATH")
	bundlePath := os.Getenv("JETKVM_TEST_BUNDLE_PATH")
	if checksumPath == "" && bundlePath == "" {
		t.Skip("release artifacts were not supplied")
	}
	payload, err := os.ReadFile(checksumPath)
	if err != nil {
		t.Fatal(err)
	}
	encodedBundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewSigstoreVerifier()
	if err := verifier.Verify(payload, encodedBundle); err != nil {
		t.Fatalf("installed updater would reject release signature: %v", err)
	}
	payload = append(payload, 'x')
	if err := verifier.Verify(payload, encodedBundle); err == nil {
		t.Fatal("release verification accepted modified checksums")
	}
}
