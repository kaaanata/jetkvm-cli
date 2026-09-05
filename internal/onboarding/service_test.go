package onboarding

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/zalando/go-keyring"
)

type memoryKeys struct {
	values map[string]string
	fail   bool
	sets   int
}

func (k *memoryKeys) Get(s, a string) (string, error) {
	if v, ok := k.values[s+"/"+a]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}
func (k *memoryKeys) Set(s, a, v string) error {
	if k.fail {
		return errors.New("secret error")
	}
	k.values[s+"/"+a] = v
	k.sets++
	return nil
}
func (k *memoryKeys) Delete(s, a string) error { delete(k.values, s+"/"+a); return nil }

func fixtureDevice(t *testing.T, id, password string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			mode := "noPassword"
			if password != "" {
				mode = "password"
				cookie, err := r.Cookie("authToken")
				if err != nil || cookie.Value != "ok" {
					w.WriteHeader(401)
					return
				}
			}
			_ = json.MarshalWrite(w, map[string]any{"deviceId": id, "authMode": mode, "loopbackOnly": false})
		case "/auth/login-local":
			body, _ := io.ReadAll(r.Body)
			if password == "" || !strings.Contains(string(body), password) {
				http.Error(w, "wrong credential", 401)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "authToken", Value: "ok", Path: "/"})
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("setup contacted unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestConnectDerivesIdentityAndIsIdempotent(t *testing.T) {
	t.Parallel()
	server := fixtureDevice(t, "fixture-one", "")
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	s, _ := New(Options{Path: path})
	req := Request{Address: server.URL, AllowHTTP: true, Control: true}
	receipt, err := s.Connect(t.Context(), req, Secret{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DeviceID != "fixture-one" || receipt.Name == "" || receipt.Status != "connected" {
		t.Fatalf("receipt=%+v", receipt)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Devices[receipt.Name]
	if d.DeviceID != "fixture-one" || !d.Takeover.RequireConfirmation || d.Credentials.Provider != config.CredentialNoPassword || !slices.Contains(d.Permissions, "input") || slices.Contains(cfg.Toolsets.Allow, "power") {
		t.Fatalf("configuration=%+v", cfg)
	}
	before, _ := os.ReadFile(path)
	again, err := s.Connect(t.Context(), req, Secret{})
	if err != nil || again.Status != "already_configured" {
		t.Fatalf("retry=%+v %v", again, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("idempotent setup changed config")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions=%v", info.Mode())
	}
}

func TestPasswordNeverEntersConfigurationOrReceipt(t *testing.T) {
	t.Parallel()
	server := fixtureDevice(t, "private-fixture", "fixture-password")
	path := filepath.Join(t.TempDir(), "config.json")
	keys := &memoryKeys{values: make(map[string]string)}
	s, _ := New(Options{Path: path, Keyring: keys})
	req := Request{Address: server.URL, AllowHTTP: true}
	if _, err := s.Connect(t.Context(), req, Secret{}); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("missing password: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed setup wrote config")
	}
	if _, err := s.Connect(t.Context(), req, Secret{Password: []byte("wrong")}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong password: %v", err)
	}
	receipt, err := s.Connect(t.Context(), req, Secret{Password: []byte("fixture-password")})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	encoded, _ := json.Marshal(receipt)
	if strings.Contains(string(data)+string(encoded), "fixture-password") {
		t.Fatal("credential leaked")
	}
	if keys.sets != 1 || len(keys.values) != 1 {
		t.Fatalf("keys=%+v", keys)
	}
	if _, err := s.Connect(t.Context(), req, Secret{Password: []byte("fixture-password")}); err != nil || keys.sets != 1 {
		t.Fatalf("repeat duplicated credential: %v", err)
	}
	secret := Secret{Password: []byte("fixture-password")}
	if strings.Contains(fmt.Sprintf("%+v", secret), "fixture-password") {
		t.Fatal("formatted credential leaked")
	}
	if _, err := json.Marshal(secret); err == nil {
		t.Fatal("secret serialized")
	}
}

func TestKeyringFailureAndInvalidConfigNeverOverwrite(t *testing.T) {
	t.Parallel()
	server := fixtureDevice(t, "private-fixture", "fixture-password")
	path := filepath.Join(t.TempDir(), "config.json")
	s, _ := New(Options{Path: path, Keyring: &memoryKeys{fail: true}})
	if _, err := s.Connect(t.Context(), Request{Address: server.URL, AllowHTTP: true}, Secret{Password: []byte("fixture-password")}); !errors.Is(err, ErrCredentialStore) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential failure created config")
	}
	if err := os.WriteFile(path, []byte(`{"foreign":"keep"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connect(t.Context(), Request{Address: server.URL, AllowHTTP: true}, Secret{Password: []byte("fixture-password")}); err == nil {
		t.Fatal("invalid config overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"foreign":"keep"}` {
		t.Fatal("foreign config lost")
	}
}

func TestConcurrentEnrollmentPreservesDevicesAndCeiling(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	one, two := fixtureDevice(t, "fixture-one", ""), fixtureDevice(t, "fixture-two", "")
	s, _ := New(Options{Path: path})
	var wg sync.WaitGroup
	for _, address := range []string{one.URL, two.URL} {
		wg.Go(func() {
			if _, err := s.Connect(t.Context(), Request{Address: address, AllowHTTP: true}, Secret{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	cfg, err := config.Load(path)
	if err != nil || len(cfg.Devices) != 2 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	three := fixtureDevice(t, "fixture-three", "")
	if _, err := s.Connect(t.Context(), Request{Address: three.URL, AllowHTTP: true, Control: true}, Secret{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ceiling widened: %v", err)
	}
}

func TestSetupTrustAndCancellation(t *testing.T) {
	t.Parallel()
	s, _ := New(Options{Path: filepath.Join(t.TempDir(), "config.json")})
	if _, err := s.Connect(t.Context(), Request{Address: "http://127.0.0.1:1"}, Secret{}); err == nil {
		t.Fatal("HTTP trust inferred")
	}
	for _, address := range []string{"", "http://user:password@localhost", "http://localhost/path", "file:///etc/passwd", "http://localhost?secret=x"} {
		if _, err := NormalizeAddress(address); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Connect(ctx, Request{Address: "http://127.0.0.1:1", AllowHTTP: true}, Secret{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
