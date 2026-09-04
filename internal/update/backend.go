package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	selfapply "github.com/creativeprojects/go-selfupdate/update"
	"github.com/google/go-github/v86/github"
)

const (
	maxReleaseArchiveBytes = 128 << 20
	maxReleaseBinaryBytes  = 64 << 20
	githubOwner            = "kaaanata"
	githubRepository       = "jetkvm-cli"
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
	HTTPClient        *http.Client
	APIBaseURL        string
	OS                string
	Arch              string
}

type githubBackend struct {
	github       *github.Client
	http         *http.Client
	signature    SignatureVerifier
	operatingSys string
	architecture string
	allowHTTP    bool
}

type releaseAssets struct {
	archiveURL  string
	checksumURL string
	bundleURL   string
}

func NewGitHubBackend(config GitHubBackendConfig) (Backend, error) {
	if config.SignatureVerifier == nil {
		return nil, newError(ErrSignatureVerification, "a Sigstore-compatible signature verifier is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	client := github.NewClient(httpClient)
	if config.Token != "" {
		client = client.WithAuthToken(config.Token)
	}
	allowHTTP := false
	if config.APIBaseURL != "" {
		base, err := url.Parse(config.APIBaseURL)
		if err != nil || !base.IsAbs() {
			return nil, newError(ErrInvalidRequest, "GitHub API base URL must be absolute")
		}
		if !strings.HasSuffix(base.Path, "/") {
			base.Path += "/"
		}
		client.BaseURL = base
		client.UploadURL = base.Clone()
		allowHTTP = base.Scheme == "http"
	}
	return &githubBackend{
		github: client, http: httpClient, signature: config.SignatureVerifier,
		operatingSys: config.OS, architecture: config.Arch, allowHTTP: allowHTTP,
	}, nil
}

func (b *githubBackend) Resolve(ctx context.Context, query ReleaseQuery) (Release, error) {
	var candidate *github.RepositoryRelease
	var err error
	switch {
	case query.Version != "":
		candidate, _, err = b.github.Repositories.GetReleaseByTag(ctx, githubOwner, githubRepository, query.Version)
	case query.Prerelease:
		candidate, err = b.latestIncludingPrerelease(ctx)
	default:
		candidate, _, err = b.github.Repositories.GetLatestRelease(ctx, githubOwner, githubRepository)
	}
	if err != nil {
		var responseError *github.ErrorResponse
		if errors.As(err, &responseError) && responseError.Response != nil && responseError.Response.StatusCode == http.StatusNotFound {
			return Release{}, &Error{Kind: ErrReleaseNotFound, Message: "release was not found", Cause: err}
		}
		return Release{}, &Error{Kind: ErrReleaseResolution, Message: "resolve GitHub release metadata", Cause: err}
	}
	if candidate == nil || candidate.GetDraft() {
		return Release{}, newError(ErrReleaseNotFound, "release was not found")
	}
	version, err := releaseVersion(candidate.GetTagName())
	if err != nil {
		return Release{}, err
	}
	if !query.Prerelease && candidate.GetPrerelease() {
		return Release{}, newError(ErrReleaseNotFound, "stable release was not found")
	}
	archiveName, err := releaseArchiveName(version, b.operatingSys, b.architecture)
	if err != nil {
		return Release{}, err
	}
	assets, err := requiredReleaseAssets(candidate.GetAssets(), archiveName)
	if err != nil {
		return Release{}, err
	}
	return Release{
		Version: version, Prerelease: candidate.GetPrerelease(), AssetName: archiveName,
		AssetURL: assets.archiveURL, PublishedAt: candidate.GetPublishedAt().Time, native: assets,
	}, nil
}

func (b *githubBackend) latestIncludingPrerelease(ctx context.Context) (*github.RepositoryRelease, error) {
	var selected *github.RepositoryRelease
	var selectedVersion *semver.Version
	options := &github.ListOptions{PerPage: 100}
	for {
		releases, response, err := b.github.Repositories.ListReleases(ctx, githubOwner, githubRepository, options)
		if err != nil {
			return nil, err
		}
		for _, release := range releases {
			if release == nil || release.GetDraft() {
				continue
			}
			version, err := semver.StrictNewVersion(strings.TrimPrefix(release.GetTagName(), "v"))
			if err != nil {
				continue
			}
			if selectedVersion == nil || version.GreaterThan(selectedVersion) {
				selected, selectedVersion = release, version
			}
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	if selected == nil {
		return nil, newError(ErrReleaseNotFound, "release was not found")
	}
	return selected, nil
}

func releaseVersion(tag string) (string, error) {
	version := strings.TrimPrefix(tag, "v")
	parsed, err := semver.StrictNewVersion(version)
	if err != nil {
		return "", newError(ErrReleaseResolution, "release tag %q is not strict semantic version", tag)
	}
	return parsed.String(), nil
}

func releaseArchiveName(version, operatingSystem, architecture string) (string, error) {
	extension := ".tar.gz"
	if operatingSystem == "windows" {
		extension = ".zip"
	}
	if !slices.Contains([]string{"darwin", "linux", "windows"}, operatingSystem) || !slices.Contains([]string{"amd64", "arm64"}, architecture) {
		return "", newError(ErrInvalidRequest, "unsupported release target %s/%s", operatingSystem, architecture)
	}
	return fmt.Sprintf("jetkvm_%s_%s_%s%s", version, operatingSystem, architecture, extension), nil
}

func requiredReleaseAssets(assets []*github.ReleaseAsset, archiveName string) (releaseAssets, error) {
	names := []string{archiveName, "checksums.txt", "checksums.txt.sigstore.json"}
	found := make(map[string]string, len(names))
	counts := make(map[string]int, len(names))
	for _, asset := range assets {
		if asset == nil || !slices.Contains(names, asset.GetName()) {
			continue
		}
		counts[asset.GetName()]++
		found[asset.GetName()] = asset.GetBrowserDownloadURL()
	}
	for _, name := range names {
		if counts[name] != 1 || found[name] == "" {
			return releaseAssets{}, newError(ErrReleaseResolution, "release must contain exactly one %s asset", name)
		}
	}
	return releaseAssets{archiveURL: found[archiveName], checksumURL: found["checksums.txt"], bundleURL: found["checksums.txt.sigstore.json"]}, nil
}

func (b *githubBackend) Apply(ctx context.Context, release Release, target, backup string) error {
	assets, ok := release.native.(releaseAssets)
	if !ok {
		return newError(ErrApplyFailed, "release does not belong to the configured backend")
	}
	checksums, err := b.download(ctx, assets.checksumURL, 4<<20)
	if err != nil {
		return err
	}
	bundle, err := b.download(ctx, assets.bundleURL, 4<<20)
	if err != nil {
		return err
	}
	if err := b.signature.Verify(checksums, bundle); err != nil {
		return &Error{Kind: ErrSignatureVerification, Message: "verify checksums signature", Cause: err}
	}
	archive, err := b.download(ctx, assets.archiveURL, maxReleaseArchiveBytes)
	if err != nil {
		return err
	}
	if err := verifyArchiveChecksum(release.AssetName, archive, checksums); err != nil {
		return err
	}
	binary, err := extractReleaseBinary(release.AssetName, archive)
	if err != nil {
		return err
	}
	if err := selfapply.Apply(bytes.NewReader(binary), selfapply.Options{TargetPath: target, OldSavePath: backup}); err != nil {
		return fmt.Errorf("replace executable with verified release: %w", err)
	}
	return selfCheckCandidate(ctx, target, release.Version, b.operatingSys, b.architecture)
}

func (b *githubBackend) download(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "https" && !(b.allowHTTP && parsed.Scheme == "http")) {
		return nil, newError(ErrReleaseResolution, "release asset URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := b.http.Do(request)
	if err != nil {
		return nil, &Error{Kind: ErrReleaseResolution, Message: "download release asset", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, newError(ErrReleaseResolution, "download release asset returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, newError(ErrReleaseResolution, "release asset exceeds size limit")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, newError(ErrReleaseResolution, "release asset exceeds size limit")
	}
	return payload, nil
}

func verifyArchiveChecksum(name string, archive, checksums []byte) error {
	var expected []byte
	count := 0
	for line := range strings.Lines(string(checksums)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return newError(ErrChecksumMismatch, "release checksum is invalid")
		}
		expected = decoded
		count++
	}
	if count != 1 {
		return newError(ErrChecksumMismatch, "release checksum must contain exactly one entry for %s", name)
	}
	actual := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return newError(ErrChecksumMismatch, "release archive checksum does not match")
	}
	return nil
}

func extractReleaseBinary(name string, archive []byte) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		return extractZipBinary(archive)
	}
	if strings.HasSuffix(name, ".tar.gz") {
		return extractTarBinary(archive)
	}
	return nil, newError(ErrApplyFailed, "unsupported release archive format")
}

func extractZipBinary(archive []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, &Error{Kind: ErrApplyFailed, Message: "open release ZIP", Cause: err}
	}
	if len(reader.File) != 4 {
		return nil, newError(ErrApplyFailed, "release archive must contain exactly four files")
	}
	expected := map[string]bool{"jetkvm.exe": false, "LICENSE": false, "NOTICE": false, "README.md": false}
	var binary []byte
	for _, file := range reader.File {
		seen, ok := expected[file.Name]
		if !ok || seen || !file.FileInfo().Mode().IsRegular() {
			return nil, newError(ErrApplyFailed, "release archive contains an unsafe or unexpected entry")
		}
		expected[file.Name] = true
		if file.Name != "jetkvm.exe" {
			continue
		}
		if file.UncompressedSize64 > maxReleaseBinaryBytes {
			return nil, newError(ErrApplyFailed, "release executable exceeds size limit")
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		binary, err = io.ReadAll(io.LimitReader(stream, maxReleaseBinaryBytes+1))
		closeErr := stream.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
	}
	return validateExtractedBinary(binary)
}

func extractTarBinary(archive []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, &Error{Kind: ErrApplyFailed, Message: "open release tarball", Cause: err}
	}
	defer gzipReader.Close()
	tape := tar.NewReader(gzipReader)
	expected := map[string]bool{"jetkvm": false, "LICENSE": false, "NOTICE": false, "README.md": false}
	var binary []byte
	count := 0
	var total int64
	for {
		header, err := tape.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &Error{Kind: ErrApplyFailed, Message: "read release tarball", Cause: err}
		}
		seen, ok := expected[header.Name]
		regular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
		if !ok || seen || !regular || header.Size < 0 {
			return nil, newError(ErrApplyFailed, "release archive contains an unsafe or unexpected entry")
		}
		expected[header.Name] = true
		count++
		total += header.Size
		if total > maxReleaseArchiveBytes || (header.Name == "jetkvm" && header.Size > maxReleaseBinaryBytes) {
			return nil, newError(ErrApplyFailed, "release archive expands beyond size limit")
		}
		if header.Name == "jetkvm" {
			binary, err = io.ReadAll(io.LimitReader(tape, maxReleaseBinaryBytes+1))
		} else {
			_, err = io.Copy(io.Discard, tape)
		}
		if err != nil {
			return nil, err
		}
	}
	if count != 4 {
		return nil, newError(ErrApplyFailed, "release archive must contain exactly four files")
	}
	return validateExtractedBinary(binary)
}

func validateExtractedBinary(binary []byte) ([]byte, error) {
	if len(binary) == 0 || len(binary) > maxReleaseBinaryBytes {
		return nil, newError(ErrApplyFailed, "release executable size is outside the allowed range")
	}
	return binary, nil
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
