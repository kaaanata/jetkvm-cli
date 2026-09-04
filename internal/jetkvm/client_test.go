package jetkvm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewClientRejectsInvalidOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    string
		allowHTTP bool
		wantError error
	}{
		{name: "missing scheme", origin: "jetkvm.local", wantError: ErrInvalidOrigin},
		{name: "unsupported scheme", origin: "ssh://jetkvm.local", wantError: ErrInvalidOrigin},
		{name: "userinfo", origin: "http://user:secret@jetkvm.local", allowHTTP: true, wantError: ErrInvalidOrigin},
		{name: "path", origin: "http://jetkvm.local/device", allowHTTP: true, wantError: ErrInvalidOrigin},
		{name: "encoded path", origin: "http://jetkvm.local/%2f", allowHTTP: true, wantError: ErrInvalidOrigin},
		{name: "query", origin: "http://jetkvm.local?token=secret", allowHTTP: true, wantError: ErrInvalidOrigin},
		{name: "fragment", origin: "http://jetkvm.local#device", allowHTTP: true, wantError: ErrInvalidOrigin},
		{name: "plain HTTP without opt-in", origin: "http://jetkvm.local", wantError: ErrPlainHTTPNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(Config{Origin: test.origin, AllowPlainHTTP: test.allowHTTP})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("NewClient() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestClientNoPasswordReadsAllEndpointsWithoutLogin(t *testing.T) {
	t.Parallel()

	var loginCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device/status":
			_, _ = w.Write([]byte(`{"isSetup":true}`))
		case "/device":
			_, _ = w.Write([]byte(`{"authMode":"noPassword","deviceId":"device-1","loopbackOnly":false}`))
		case "/cloud/state":
			_, _ = w.Write([]byte(`{"connected":true,"url":"wss://cloud.example","appUrl":"https://cloud.example"}`))
		case "/auth/login-local":
			loginCalls.Add(1)
			http.Error(w, "unexpected login", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newHTTPTestClient(t, server.URL, nil)
	setup, err := client.GetDeviceStatus(t.Context())
	if err != nil || !setup.IsSetup {
		t.Fatalf("GetDeviceStatus() = %+v, %v", setup, err)
	}
	local, err := client.GetDevice(t.Context())
	if err != nil {
		t.Fatalf("GetDevice() error = %v", err)
	}
	if local.AuthMode != "noPassword" || local.DeviceID != "device-1" || local.LoopbackOnly {
		t.Fatalf("GetDevice() = %+v", local)
	}
	cloud, err := client.GetCloudState(t.Context())
	if err != nil {
		t.Fatalf("GetCloudState() error = %v", err)
	}
	if !cloud.Connected || cloud.URL != "wss://cloud.example" || cloud.AppURL != "https://cloud.example" {
		t.Fatalf("GetCloudState() = %+v", cloud)
	}
	if got := loginCalls.Load(); got != 0 {
		t.Fatalf("login calls = %d, want 0", got)
	}
}

func TestClientPasswordLoginUsesCookieAndDoesNotLeakCredential(t *testing.T) {
	t.Parallel()

	const password = "highly-sensitive-password"
	var loginCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			cookie, err := r.Cookie("authToken")
			if err != nil || cookie.Value != "session-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"authMode":"password","deviceId":"device-1","loopbackOnly":false}`))
		case "/auth/login-local":
			loginCalls.Add(1)
			payload, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(payload), password) {
				http.Error(w, "wrong password", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "authToken", Value: "session-token", Path: "/", HttpOnly: true})
			_, _ = w.Write([]byte(`{"message":"Login successful"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := CredentialProviderFunc(func(context.Context) ([]byte, error) {
		return []byte(password), nil
	})
	client := newHTTPTestClient(t, server.URL, provider)
	device, err := client.GetDevice(t.Context())
	if err != nil {
		t.Fatalf("GetDevice() error = %v", err)
	}
	if device.AuthMode != "password" {
		t.Fatalf("auth mode = %q, want password", device.AuthMode)
	}
	if got := loginCalls.Load(); got != 1 {
		t.Fatalf("login calls = %d, want 1", got)
	}
}

func TestClientAuthenticationFailureRedactsCredential(t *testing.T) {
	t.Parallel()

	const password = "must-not-appear"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/device" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"must-not-appear"}`))
	}))
	defer server.Close()

	client := newHTTPTestClient(t, server.URL, CredentialProviderFunc(func(context.Context) ([]byte, error) {
		return []byte(password), nil
	}))
	_, err := client.GetDevice(t.Context())
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("GetDevice() error = %v, want authentication failure", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("credential leaked in error: %v", err)
	}
}

func TestClientRejectsCrossOriginRedirect(t *testing.T) {
	t.Parallel()

	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationRequests.Add(1)
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/device", http.StatusFound)
	}))
	defer source.Close()

	client := newHTTPTestClient(t, source.URL, nil)
	_, err := client.GetDevice(t.Context())
	if !errors.Is(err, ErrCrossOriginRedirect) {
		t.Fatalf("GetDevice() error = %v, want cross-origin redirect", err)
	}
	if got := destinationRequests.Load(); got != 0 {
		t.Fatalf("redirect destination requests = %d, want 0", got)
	}
}

func TestClientRejectsMissingRequiredFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authMode":"noPassword","deviceId":"device-1"}`))
	}))
	defer server.Close()

	client := newHTTPTestClient(t, server.URL, nil)
	_, err := client.GetDevice(t.Context())
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("GetDevice() error = %v, want unexpected response", err)
	}
}

func newHTTPTestClient(t *testing.T, origin string, credentials CredentialProvider) *Client {
	t.Helper()
	client, err := NewClient(Config{
		Origin:         origin,
		AllowPlainHTTP: true,
		Credentials:    credentials,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}
