package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBuiltReleaseArchiveCompatibility gates actual GoReleaser archives before
// signing. It uses the unchanged production extractors; no candidate is run.
func TestBuiltReleaseArchiveCompatibility(t *testing.T) {
	dist := os.Getenv("JETKVM_TEST_RELEASE_DIST")
	if dist == "" {
		t.Skip("set JETKVM_TEST_RELEASE_DIST to check built release archives")
	}
	manifest := readCompatFile(t, filepath.Join(dist, "checksums.txt"))
	licenses := readCompatFile(t, "../video/DECODER_LICENSES.txt")
	sbom := readCompatFile(t, "../../.cache/release-metadata/decoder.sbom.json")
	if err := verifyArchiveChecksum("decoder.sbom.json", sbom, manifest); err != nil {
		t.Fatal(err)
	}
	source := readCompatFile(t, "../../.cache/release-metadata/decoder-source.tar.gz")
	if err := verifyArchiveChecksum("decoder-source.tar.gz", source, manifest); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Packages []struct {
			Name    string `json:"name"`
			Version string `json:"versionInfo"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(sbom, &document); err != nil {
		t.Fatal(err)
	}
	for _, dependency := range []struct{ name, version string }{{"ffmpeg", "9.0.1"}} {
		found := false
		for _, pkg := range document.Packages {
			if pkg.Name == dependency.name && pkg.Version == dependency.version {
				found = true
			}
		}
		if !found {
			t.Fatalf("decoder SBOM missing %s %s", dependency.name, dependency.version)
		}
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^jetkvm_.+_(darwin|linux|windows)_(amd64|arm64)\.(tar\.gz|zip)$`)
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		m := pattern.FindStringSubmatch(name)
		if m == nil {
			if strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip") {
				t.Fatalf("unexpected release archive: %s", name)
			}
			continue
		}
		platform := m[1] + "_" + m[2]
		if seen[platform] {
			t.Fatalf("duplicate archive for %s", platform)
		}
		seen[platform] = true
		t.Run(platform, func(t *testing.T) {
			data := readCompatFile(t, filepath.Join(dist, name))
			if err := verifyArchiveChecksum(name, data, manifest); err != nil {
				t.Fatal(err)
			}
			var binary, notice []byte
			var err error
			executable := "jetkvm"
			if m[1] == "windows" {
				if m[3] != "zip" {
					t.Fatal("Windows requires zip")
				}
				executable += ".exe"
				binary, err = extractZipBinary(data)
				if err != nil {
					t.Fatal(err)
				}
				reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range reader.File {
					if f.Name == "NOTICE" {
						r, err := f.Open()
						if err != nil {
							t.Fatal(err)
						}
						notice, err = io.ReadAll(r)
						_ = r.Close()
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				if m[3] != "tar.gz" {
					t.Fatal("Unix requires tar.gz")
				}
				binary, err = extractTarBinary(data)
				if err != nil {
					t.Fatal(err)
				}
				gz, err := gzip.NewReader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				tr := tar.NewReader(gz)
				for {
					h, err := tr.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if h.Name == "NOTICE" {
						notice, err = io.ReadAll(tr)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if !bytes.Contains(notice, licenses) {
				t.Fatal("archive NOTICE omits full codec license")
			}
			builds, err := filepath.Glob(filepath.Join(dist, "jetkvm_"+platform+"*", executable))
			if err != nil || len(builds) != 1 {
				t.Fatalf("expected one source executable: %v %v", builds, err)
			}
			if !bytes.Equal(binary, readCompatFile(t, builds[0])) {
				t.Fatal("production extractor did not preserve built executable bytes")
			}
		})
	}
	for _, osName := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if !seen[osName+"_"+arch] {
				t.Errorf("missing archive: %s_%s", osName, arch)
			}
		}
	}
}

func readCompatFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestArchiveCompatibilityRejectsDecoderSidecars(t *testing.T) {
	for _, extra := range []string{"DECODER_LICENSES.txt", "decoder/go.mod", "decoder/go.sum"} {
		t.Run(extra, func(t *testing.T) {
			files := map[string][]byte{"jetkvm": bytes.Repeat([]byte("binary"), 1<<19), "LICENSE": []byte("license"), "NOTICE": []byte("notice"), "README.md": []byte("readme"), extra: []byte("sidecar")}
			if _, err := extractTarBinary(testTarGzip(t, files, "")); err == nil {
				t.Fatal("accepted extra tar entry")
			}
			files["jetkvm.exe"] = files["jetkvm"]
			delete(files, "jetkvm")
			if _, err := extractZipBinary(testZip(t, files)); err == nil {
				t.Fatal("accepted extra zip entry")
			}
		})
	}
}
