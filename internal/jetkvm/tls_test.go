package jetkvm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPinnedHTTPClient(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"isSetup":true}`))
	}))
	defer server.Close()

	digest := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	httpClient, err := NewPinnedHTTPClient(hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{Origin: server.URL, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.GetDeviceStatus(t.Context())
	if err != nil || !status.IsSetup {
		t.Fatalf("GetDeviceStatus() = (%+v, %v)", status, err)
	}

	wrongClient, err := NewPinnedHTTPClient(hex.EncodeToString(make([]byte, sha256.Size)))
	if err != nil {
		t.Fatal(err)
	}
	client, err = NewClient(Config{Origin: server.URL, HTTPClient: wrongClient})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetDeviceStatus(t.Context()); !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("GetDeviceStatus() error = %v, want request failure", err)
	}
}

func TestPinnedHTTPClientRejectsMalformedPin(t *testing.T) {
	t.Parallel()
	if _, err := NewPinnedHTTPClient("not-a-pin"); !errors.Is(err, ErrInvalidSPKIPin) {
		t.Fatalf("NewPinnedHTTPClient() error = %v, want invalid pin", err)
	}
}
