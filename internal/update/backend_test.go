package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGitHubBackendResolvesClosedReleaseAssets(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/kaaanata/jetkvm-cli/releases/latest" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(response, `{"tag_name":"v1.2.3","draft":false,"prerelease":false,"published_at":"2026-09-05T00:00:00Z","assets":[
			{"name":"jetkvm_1.2.3_linux_amd64.tar.gz","browser_download_url":"%s/archive"},
			{"name":"checksums.txt","browser_download_url":"%s/checksums"},
			{"name":"checksums.txt.sigstore.json","browser_download_url":"%s/bundle"}]}`, server.URL, server.URL, server.URL)
	}))
	defer server.Close()

	backend, err := NewGitHubBackend(GitHubBackendConfig{
		SignatureVerifier: signatureVerifierFunc(func([]byte, []byte) error { return nil }),
		HTTPClient:        server.Client(), APIBaseURL: server.URL, OS: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err := backend.Resolve(t.Context(), ReleaseQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "1.2.3" || release.AssetName != "jetkvm_1.2.3_linux_amd64.tar.gz" {
		t.Fatalf("release = %+v", release)
	}
}

func TestGitHubBackendRequiresSignatureVerifier(t *testing.T) {
	_, err := NewGitHubBackend(GitHubBackendConfig{})
	if kindOf(err) != ErrSignatureVerification {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrSignatureVerification)
	}
}

func TestVerifyArchiveChecksumRequiresOneMatchingDigest(t *testing.T) {
	archive := []byte("archive")
	hash := sha256.Sum256(archive)
	checksums := []byte(fmt.Sprintf("%x  artifact.tar.gz\n", hash))
	if err := verifyArchiveChecksum("artifact.tar.gz", archive, checksums); err != nil {
		t.Fatal(err)
	}
	if kind := kindOf(verifyArchiveChecksum("artifact.tar.gz", []byte("changed"), checksums)); kind != ErrChecksumMismatch {
		t.Fatalf("kind = %q, want %q", kind, ErrChecksumMismatch)
	}
	duplicate := append(bytes.Clone(checksums), checksums...)
	if err := verifyArchiveChecksum("artifact.tar.gz", archive, duplicate); err == nil {
		t.Fatal("duplicate checksum entry was accepted")
	}
}

func TestExtractReleaseBinaryAcceptsExpectedTarAndZip(t *testing.T) {
	files := map[string][]byte{
		"jetkvm": []byte("binary"), "LICENSE": []byte("license"),
		"NOTICE": []byte("notice"), "README.md": []byte("readme"),
	}
	binary, err := extractTarBinary(testTarGzip(t, files, ""))
	if err != nil || string(binary) != "binary" {
		t.Fatalf("tar binary = %q, error = %v", binary, err)
	}
	zipFiles := map[string][]byte{
		"jetkvm.exe": []byte("binary"), "LICENSE": []byte("license"),
		"NOTICE": []byte("notice"), "README.md": []byte("readme"),
	}
	binary, err = extractZipBinary(testZip(t, zipFiles))
	if err != nil || string(binary) != "binary" {
		t.Fatalf("zip binary = %q, error = %v", binary, err)
	}
}

func TestExtractReleaseBinaryRejectsUnexpectedAndLinkedEntries(t *testing.T) {
	files := map[string][]byte{
		"jetkvm": []byte("binary"), "LICENSE": []byte("license"),
		"NOTICE": []byte("notice"), "../README.md": []byte("readme"),
	}
	if _, err := extractTarBinary(testTarGzip(t, files, "")); err == nil {
		t.Fatal("tar traversal entry was accepted")
	}
	files = map[string][]byte{
		"jetkvm": []byte("binary"), "LICENSE": []byte("license"),
		"NOTICE": []byte("notice"), "README.md": []byte("readme"),
	}
	if _, err := extractTarBinary(testTarGzip(t, files, "jetkvm")); err == nil {
		t.Fatal("tar symbolic link was accepted")
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

func testTarGzip(t *testing.T, files map[string][]byte, linked string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, payload := range files {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(payload)), Typeflag: tar.TypeReg}
		if name == linked {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = "other"
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, payload := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type signatureVerifierFunc func([]byte, []byte) error

func (f signatureVerifierFunc) Verify(payload, bundle []byte) error { return f(payload, bundle) }
