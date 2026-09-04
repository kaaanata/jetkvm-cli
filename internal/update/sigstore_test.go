package update

import (
	"errors"
	"os"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
)

func TestPublicGoodTrustedRootLoader(t *testing.T) {
	if os.Getenv("JETKVM_TEST_SIGSTORE_TUF") != "1" {
		t.Skip("set JETKVM_TEST_SIGSTORE_TUF=1 to exercise the public-good TUF repository")
	}
	trustedMaterial, err := (publicGoodTrustedRootLoader{}).Load()
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if len(trustedMaterial.FulcioCertificateAuthorities()) == 0 {
		t.Fatal("trusted root has no Fulcio certificate authorities")
	}
	if len(trustedMaterial.RekorLogs()) == 0 {
		t.Fatal("trusted root has no Rekor logs")
	}
	if len(trustedMaterial.CTLogs()) == 0 {
		t.Fatal("trusted root has no certificate transparency logs")
	}
}

func TestNewSigstoreVerifierUsesProductionPolicy(t *testing.T) {
	verifier, ok := NewSigstoreVerifier().(*sigstoreVerifier)
	if !ok {
		t.Fatal("NewSigstoreVerifier returned an unexpected implementation")
	}
	if _, ok := verifier.roots.(publicGoodTrustedRootLoader); !ok {
		t.Fatal("production verifier does not use the public-good TUF loader")
	}
	if _, ok := verifier.engine.(sigstoreBundleVerifier); !ok {
		t.Fatal("production verifier does not use sigstore-go")
	}
	if githubActionsOIDCIssuer != "https://token.actions.githubusercontent.com" {
		t.Fatalf("unexpected OIDC issuer: %q", githubActionsOIDCIssuer)
	}
	if releaseWorkflowIdentity != "https://github.com/kaaanata/jetkvm-cli/.github/workflows/release.yml@refs/heads/main" {
		t.Fatalf("unexpected workflow identity: %q", releaseWorkflowIdentity)
	}
}

func TestSigstoreVerifierDelegatesExactPayloadAndBundle(t *testing.T) {
	trustedMaterial := &root.BaseTrustedMaterial{}
	engine := &recordingBundleVerifier{}
	verifier := &sigstoreVerifier{
		roots:  staticTrustedRootLoader{trustedMaterial: trustedMaterial},
		engine: engine,
	}
	payload := []byte("abc  checksums.txt\n")
	encodedBundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)

	if err := verifier.Verify(payload, encodedBundle); err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if string(engine.payload) != string(payload) {
		t.Fatalf("payload changed: got %q, want %q", engine.payload, payload)
	}
	if string(engine.encodedBundle) != string(encodedBundle) {
		t.Fatalf("bundle changed: got %q, want %q", engine.encodedBundle, encodedBundle)
	}
	if engine.trustedMaterial != trustedMaterial {
		t.Fatal("trusted material was not passed to the verification engine")
	}
}

func TestSigstoreVerifierFailsClosed(t *testing.T) {
	rootFailure := errors.New("TUF unavailable")
	tests := []struct {
		name     string
		verifier *sigstoreVerifier
		payload  []byte
		bundle   []byte
		want     string
	}{
		{
			name:     "empty payload",
			verifier: &sigstoreVerifier{roots: staticTrustedRootLoader{}, engine: &recordingBundleVerifier{}},
			bundle:   []byte("bundle"),
			want:     "checksums payload is empty",
		},
		{
			name:     "empty bundle",
			verifier: &sigstoreVerifier{roots: staticTrustedRootLoader{}, engine: &recordingBundleVerifier{}},
			payload:  []byte("payload"),
			want:     "Sigstore bundle is empty",
		},
		{
			name:     "missing configuration",
			verifier: &sigstoreVerifier{},
			payload:  []byte("payload"),
			bundle:   []byte("bundle"),
			want:     "Sigstore verifier is not configured",
		},
		{
			name:     "TUF failure",
			verifier: &sigstoreVerifier{roots: staticTrustedRootLoader{err: rootFailure}, engine: &recordingBundleVerifier{}},
			payload:  []byte("payload"),
			bundle:   []byte("bundle"),
			want:     rootFailure.Error(),
		},
		{
			name:     "nil trust material",
			verifier: &sigstoreVerifier{roots: staticTrustedRootLoader{}, engine: &recordingBundleVerifier{}},
			payload:  []byte("payload"),
			bundle:   []byte("bundle"),
			want:     "Sigstore trusted root loader returned no trust material",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.verifier.Verify(test.payload, test.bundle)
			if err == nil || err.Error() != test.want {
				t.Fatalf("Verify error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSigstoreBundleVerifierRejectsMalformedOrWrongBundleKind(t *testing.T) {
	engine := sigstoreBundleVerifier{}
	trustedMaterial := &root.BaseTrustedMaterial{}
	tests := []struct {
		name   string
		bundle string
	}{
		{name: "malformed JSON", bundle: "{"},
		{
			name: "unsupported old bundle",
			bundle: `{
				"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.1",
				"verificationMaterial":{"publicKey":{"hint":"test"}},
				"messageSignature":{"messageDigest":{"algorithm":"SHA2_256","digest":"AA=="},"signature":"AA=="}
			}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := engine.Verify([]byte("payload"), []byte(test.bundle), trustedMaterial); err == nil {
				t.Fatal("Verify unexpectedly accepted an invalid bundle")
			}
		})
	}
}

type staticTrustedRootLoader struct {
	trustedMaterial root.TrustedMaterial
	err             error
}

func (l staticTrustedRootLoader) Load() (root.TrustedMaterial, error) {
	return l.trustedMaterial, l.err
}

type recordingBundleVerifier struct {
	payload         []byte
	encodedBundle   []byte
	trustedMaterial root.TrustedMaterial
}

func (v *recordingBundleVerifier) Verify(payload, encodedBundle []byte, trustedMaterial root.TrustedMaterial) error {
	v.payload = payload
	v.encodedBundle = encodedBundle
	v.trustedMaterial = trustedMaterial
	return nil
}
