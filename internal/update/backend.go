package update

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	selfapply "github.com/creativeprojects/go-selfupdate/update"
)

type ReleaseQuery struct {
	Version    string
	Prerelease bool
}

type Backend interface {
	Resolve(context.Context, ReleaseQuery) (Release, error)
	Apply(context.Context, Release, string, string) error
	ReplaceFromFile(context.Context, string, string, string) error
}

type SignatureVerifier interface {
	Verify(payload, bundle []byte) error
}

type GitHubBackendConfig struct {
	Token             string
	SignatureVerifier SignatureVerifier
	OS                string
	Arch              string
}

type githubBackend struct {
	source selfupdate.Source
	config GitHubBackendConfig
	repo   selfupdate.Repository
}

func NewGitHubBackend(config GitHubBackendConfig) (Backend, error) {
	if config.SignatureVerifier == nil {
		return nil, newError(ErrSignatureVerification, "a Sigstore-compatible signature verifier is required")
	}
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{APIToken: config.Token})
	if err != nil {
		return nil, err
	}
	return &githubBackend{
		source: source,
		config: config,
		repo:   selfupdate.NewRepositorySlug("kaaanata", "jetkvm-cli"),
	}, nil
}

func (b *githubBackend) updater(prerelease bool, oldSavePath string) (*selfupdate.Updater, error) {
	return selfupdate.NewUpdater(selfupdate.Config{
		Source:      b.source,
		Validator:   newReleaseValidator(b.config.SignatureVerifier),
		OS:          b.config.OS,
		Arch:        b.config.Arch,
		Prerelease:  prerelease,
		OldSavePath: oldSavePath,
	})
}

func (b *githubBackend) Resolve(ctx context.Context, query ReleaseQuery) (Release, error) {
	updater, err := b.updater(query.Prerelease, "")
	if err != nil {
		return Release{}, &Error{Kind: ErrReleaseResolution, Message: "resolve GitHub release", Cause: err}
	}
	var candidate *selfupdate.Release
	var found bool
	if query.Version == "" {
		candidate, found, err = updater.DetectLatest(ctx, b.repo)
	} else {
		candidate, found, err = updater.DetectVersion(ctx, b.repo, query.Version)
	}
	if err != nil {
		kind := ErrReleaseResolution
		if errors.Is(err, selfupdate.ErrValidationAssetNotFound) {
			kind = ErrSignatureVerification
		}
		return Release{}, &Error{Kind: kind, Message: "resolve verified release metadata", Cause: err}
	}
	if !found {
		return Release{}, newError(ErrReleaseNotFound, "release %q was not found", query.Version)
	}
	return Release{
		Version: candidate.Version(), Prerelease: candidate.Prerelease,
		AssetName: candidate.AssetName, AssetURL: candidate.AssetURL,
		PublishedAt: candidate.PublishedAt, native: candidate,
	}, nil
}

func (b *githubBackend) Apply(ctx context.Context, release Release, target, backup string) error {
	candidate, ok := release.native.(*selfupdate.Release)
	if !ok || candidate == nil {
		return newError(ErrApplyFailed, "release does not belong to the configured backend")
	}
	updater, err := b.updater(release.Prerelease, backup)
	if err != nil {
		return err
	}
	if err := updater.UpdateTo(ctx, candidate, target); err != nil {
		return err
	}
	return selfCheckCandidate(ctx, target, release.Version, b.config.OS, b.config.Arch)
}

func selfCheckCandidate(ctx context.Context, executable, version, operatingSystem, architecture string) error {
	command := exec.CommandContext(ctx, executable, "version", "--output=json")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("run updated binary self-check: %w", err)
	}
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Data          struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
			Date    string `json:"date"`
			Go      string `json:"go"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &envelope, json.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("decode updated binary self-check: %w", err)
	}
	wantedVersion := strings.TrimPrefix(version, "v")
	if envelope.SchemaVersion != "v1" || envelope.Command != "version" || envelope.Data.Version != wantedVersion ||
		envelope.Data.Commit == "" || envelope.Data.OS != operatingSystem || envelope.Data.Arch != architecture {
		return fmt.Errorf("updated binary identity mismatch: version=%q os=%q arch=%q", envelope.Data.Version, envelope.Data.OS, envelope.Data.Arch)
	}
	return nil
}

func (b *githubBackend) ReplaceFromFile(ctx context.Context, source, target, backup string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := selfapply.Apply(file, selfapply.Options{TargetPath: target, OldSavePath: backup}); err != nil {
		return fmt.Errorf("replace executable from verified local backup: %w", err)
	}
	return nil
}

type releaseValidator struct {
	checksum  selfupdate.ChecksumValidator
	signature SignatureVerifier
}

func newReleaseValidator(signature SignatureVerifier) selfupdate.Validator {
	return &releaseValidator{
		checksum:  selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
		signature: signature,
	}
}

func (v *releaseValidator) GetValidationAssetName(filename string) string {
	if filename == "checksums.txt" && v.signature != nil {
		return "checksums.txt.sigstore.json"
	}
	return "checksums.txt"
}

func (v *releaseValidator) MustContinueValidation(filename string) bool {
	return filename == "checksums.txt" && v.signature != nil
}

func (v *releaseValidator) Validate(filename string, payload, evidence []byte) error {
	if filename == "checksums.txt" {
		if v.signature == nil {
			return newError(ErrSignatureVerification, "signature verifier is not configured")
		}
		if err := v.signature.Verify(payload, evidence); err != nil {
			return &Error{Kind: ErrSignatureVerification, Message: "verify checksums signature", Cause: err}
		}
		return nil
	}
	if err := v.checksum.Validate(filename, payload, evidence); err != nil {
		return &Error{Kind: ErrChecksumMismatch, Message: "verify release checksum", Cause: err}
	}
	return nil
}
