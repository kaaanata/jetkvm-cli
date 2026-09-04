package update

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"
	releaseWorkflowIdentity = "https://github.com/kaaanata/jetkvm-cli/.github/workflows/release.yml@refs/heads/main"
	cosignBundleVersion     = "v0.3"
)

// NewSigstoreVerifier returns the production verifier for JetKVM release
// checksums. Trust material is loaded from Sigstore's public-good TUF
// repository for every verification operation so key rotations are honored.
func NewSigstoreVerifier() SignatureVerifier {
	return &sigstoreVerifier{
		roots:  publicGoodTrustedRootLoader{},
		engine: sigstoreBundleVerifier{},
	}
}

type trustedRootLoader interface {
	Load() (root.TrustedMaterial, error)
}

type publicGoodTrustedRootLoader struct{}

func (publicGoodTrustedRootLoader) Load() (root.TrustedMaterial, error) {
	client, err := tuf.DefaultClient()
	if err != nil {
		return nil, fmt.Errorf("initialize Sigstore public-good TUF client: %w", err)
	}
	trustedRoot, err := root.GetTrustedRoot(client)
	if err != nil {
		return nil, fmt.Errorf("load Sigstore public-good trusted root: %w", err)
	}
	return trustedRoot, nil
}

type bundleVerificationEngine interface {
	Verify(payload, encodedBundle []byte, trustedMaterial root.TrustedMaterial) error
}

type sigstoreVerifier struct {
	roots  trustedRootLoader
	engine bundleVerificationEngine
}

var _ SignatureVerifier = (*sigstoreVerifier)(nil)

func (v *sigstoreVerifier) Verify(payload, encodedBundle []byte) error {
	if len(payload) == 0 {
		return errors.New("checksums payload is empty")
	}
	if len(encodedBundle) == 0 {
		return errors.New("Sigstore bundle is empty")
	}
	if v == nil || v.roots == nil || v.engine == nil {
		return errors.New("Sigstore verifier is not configured")
	}

	trustedMaterial, err := v.roots.Load()
	if err != nil {
		return err
	}
	if trustedMaterial == nil {
		return errors.New("Sigstore trusted root loader returned no trust material")
	}
	return v.engine.Verify(payload, encodedBundle, trustedMaterial)
}

type sigstoreBundleVerifier struct{}

func (sigstoreBundleVerifier) Verify(payload, encodedBundle []byte, trustedMaterial root.TrustedMaterial) error {
	var signedBundle bundle.Bundle
	if err := signedBundle.UnmarshalJSON(encodedBundle); err != nil {
		return fmt.Errorf("parse cosign bundle: %w", err)
	}
	version, err := signedBundle.Version()
	if err != nil {
		return fmt.Errorf("read cosign bundle version: %w", err)
	}
	if version != cosignBundleVersion {
		return fmt.Errorf("cosign bundle version must be %s, got %s", cosignBundleVersion, version)
	}
	signatureContent, err := signedBundle.SignatureContent()
	if err != nil {
		return fmt.Errorf("read cosign bundle signature: %w", err)
	}
	if _, ok := signatureContent.(verify.MessageSignatureContent); !ok {
		return errors.New("cosign bundle must contain a message signature")
	}

	identity, err := verify.NewShortCertificateIdentity(
		githubActionsOIDCIssuer,
		"",
		releaseWorkflowIdentity,
		"",
	)
	if err != nil {
		return fmt.Errorf("configure release workflow identity: %w", err)
	}
	verifier, err := verify.NewVerifier(
		trustedMaterial,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("configure Sigstore verification: %w", err)
	}
	if _, err := verifier.Verify(
		&signedBundle,
		verify.NewPolicy(
			verify.WithArtifact(bytes.NewReader(payload)),
			verify.WithCertificateIdentity(identity),
		),
	); err != nil {
		return fmt.Errorf("verify checksums Sigstore bundle: %w", err)
	}
	return nil
}
