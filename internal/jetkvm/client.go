// Package jetkvm implements the version-sensitive local JetKVM HTTP protocol.
package jetkvm

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes   = 1 << 20
	defaultHTTPTimeout = 10 * time.Second
)

var (
	ErrInvalidOrigin         = errors.New("invalid JetKVM origin")
	ErrPlainHTTPNotAllowed   = errors.New("plain HTTP is not allowed for this device")
	ErrCrossOriginRedirect   = errors.New("cross-origin redirect is not allowed")
	ErrCredentialUnavailable = errors.New("JetKVM credential is unavailable")
	ErrAuthenticationFailed  = errors.New("JetKVM authentication failed")
	ErrRequestFailed         = errors.New("JetKVM request failed")
	ErrUnexpectedResponse    = errors.New("unexpected JetKVM response")
)

// CredentialProvider resolves a password without placing it in configuration,
// request parameters, logs, or returned errors. The caller clears the returned
// byte slice after use.
type CredentialProvider interface {
	Password(context.Context) ([]byte, error)
}

// CredentialProviderFunc adapts a function to CredentialProvider.
type CredentialProviderFunc func(context.Context) ([]byte, error)

func (f CredentialProviderFunc) Password(ctx context.Context) ([]byte, error) {
	return f(ctx)
}

// Config defines one local JetKVM HTTP endpoint.
type Config struct {
	Origin         string
	AllowPlainHTTP bool
	Credentials    CredentialProvider
	HTTPClient     *http.Client
}

// Client is safe for concurrent read-only HTTP operations.
type Client struct {
	origin      *url.URL
	http        *http.Client
	credentials CredentialProvider
	authMu      sync.Mutex
	openMu      sync.Mutex
	sessionMu   sync.Mutex
	generation  uint64
	active      *Session
}

// DeviceSetup is the public setup status returned by /device/status.
type DeviceSetup struct {
	IsSetup bool
}

// LocalDevice is the authenticated local identity returned by /device.
type LocalDevice struct {
	AuthMode     string
	DeviceID     string
	LoopbackOnly bool
}

// CloudState is the authenticated connection state returned by /cloud/state.
type CloudState struct {
	Connected bool
	URL       string
	AppURL    string
}

// HTTPStatusError reports only the status code. Response bodies are
// intentionally excluded because upstream error bodies are not a safe logging
// boundary.
type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("JetKVM returned HTTP status %d", e.StatusCode)
}

// NewClient validates the exact origin and constructs an isolated cookie jar.
func NewClient(cfg Config) (*Client, error) {
	origin, err := parseOrigin(cfg.Origin)
	if err != nil {
		return nil, err
	}
	if origin.Scheme == "http" && !cfg.AllowPlainHTTP {
		return nil, ErrPlainHTTPNotAllowed
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create JetKVM cookie jar: %w", err)
	}

	transportClient := http.DefaultClient
	if cfg.HTTPClient != nil {
		transportClient = cfg.HTTPClient
	}
	client := *transportClient
	if client.Timeout == 0 {
		client.Timeout = defaultHTTPTimeout
	}
	previousRedirectPolicy := client.CheckRedirect
	client.Jar = jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.User != nil || !sameOrigin(origin, req.URL) {
			return ErrCrossOriginRedirect
		}
		if len(via) >= 10 {
			return errors.New("too many JetKVM redirects")
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(req, via)
		}
		return nil
	}

	return &Client{
		origin:      origin,
		http:        &client,
		credentials: cfg.Credentials,
	}, nil
}

// CanonicalOrigin validates an origin and returns its canonical root form.
// Configuration and protocol layers use this function as the single origin
// normalization authority.
func CanonicalOrigin(raw string) (string, error) {
	origin, err := parseOrigin(raw)
	if err != nil {
		return "", err
	}
	return origin.String(), nil
}

// Origin returns the canonical configured origin without credentials, query,
// fragment, or path.
func (c *Client) Origin() string {
	return c.origin.String()
}

// GetDeviceStatus reads the public /device/status endpoint.
func (c *Client) GetDeviceStatus(ctx context.Context) (DeviceSetup, error) {
	var wire struct {
		IsSetup *bool `json:"isSetup"`
	}
	if err := c.get(ctx, "/device/status", false, &wire); err != nil {
		return DeviceSetup{}, err
	}
	if wire.IsSetup == nil {
		return DeviceSetup{}, ErrUnexpectedResponse
	}
	return DeviceSetup{IsSetup: *wire.IsSetup}, nil
}

// GetDevice reads identity and local authentication state from /device.
func (c *Client) GetDevice(ctx context.Context) (LocalDevice, error) {
	var wire struct {
		AuthMode     *string `json:"authMode"`
		DeviceID     string  `json:"deviceId"`
		LoopbackOnly *bool   `json:"loopbackOnly"`
	}
	if err := c.get(ctx, "/device", true, &wire); err != nil {
		return LocalDevice{}, err
	}
	if wire.AuthMode == nil || wire.DeviceID == "" || wire.LoopbackOnly == nil {
		return LocalDevice{}, ErrUnexpectedResponse
	}
	if *wire.AuthMode != "password" && *wire.AuthMode != "noPassword" {
		return LocalDevice{}, ErrUnexpectedResponse
	}
	return LocalDevice{
		AuthMode:     *wire.AuthMode,
		DeviceID:     wire.DeviceID,
		LoopbackOnly: *wire.LoopbackOnly,
	}, nil
}

// GetCloudState reads the local device's cloud connection state. It does not
// contact JetKVM Cloud.
func (c *Client) GetCloudState(ctx context.Context) (CloudState, error) {
	var wire struct {
		Connected *bool  `json:"connected"`
		URL       string `json:"url"`
		AppURL    string `json:"appUrl"`
	}
	if err := c.get(ctx, "/cloud/state", true, &wire); err != nil {
		return CloudState{}, err
	}
	if wire.Connected == nil {
		return CloudState{}, ErrUnexpectedResponse
	}
	return CloudState{
		Connected: *wire.Connected,
		URL:       wire.URL,
		AppURL:    wire.AppURL,
	}, nil
}

func (c *Client) get(ctx context.Context, path string, authenticated bool, out any) error {
	err := c.doJSON(ctx, http.MethodGet, path, nil, out)
	if !authenticated || !hasStatus(err, http.StatusUnauthorized) {
		return err
	}

	c.authMu.Lock()
	defer c.authMu.Unlock()

	// A concurrent request may have refreshed the process-local cookie while
	// this request waited for the authentication authority.
	if err := c.doJSON(ctx, http.MethodGet, path, nil, out); !hasStatus(err, http.StatusUnauthorized) {
		return err
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) login(ctx context.Context) error {
	if c.credentials == nil {
		return ErrCredentialUnavailable
	}
	password, err := c.credentials.Password(ctx)
	if err != nil || len(password) == 0 {
		clear(password)
		return ErrCredentialUnavailable
	}
	defer clear(password)

	body, err := json.Marshal(struct {
		Password string `json:"password"`
	}{Password: string(password)})
	if err != nil {
		return ErrCredentialUnavailable
	}
	defer clear(body)

	if err := c.doJSON(ctx, http.MethodPost, "/auth/login-local", body, nil); err != nil {
		if hasStatus(err, http.StatusUnauthorized) || hasStatus(err, http.StatusBadRequest) {
			return ErrAuthenticationFailed
		}
		return err
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, out any) error {
	endpoint := c.origin.Clone()
	endpoint.Path = path

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return fmt.Errorf("create JetKVM request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			return ctxErr
		}
		return &requestError{cause: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	if out == nil {
		_, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return err
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read JetKVM response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return ErrUnexpectedResponse
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return ErrUnexpectedResponse
	}
	return nil
}

type requestError struct {
	cause error
}

func (*requestError) Error() string { return ErrRequestFailed.Error() }

func (e *requestError) Unwrap() []error { return []error{ErrRequestFailed, e.cause} }

func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" || u.User != nil {
		return nil, ErrInvalidOrigin
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrInvalidOrigin
	}
	if (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return nil, ErrInvalidOrigin
	}
	u.Path = ""
	return u, nil
}

func sameOrigin(expected, actual *url.URL) bool {
	return strings.EqualFold(expected.Scheme, actual.Scheme) &&
		strings.EqualFold(expected.Hostname(), actual.Hostname()) &&
		effectivePort(expected) == effectivePort(actual)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func hasStatus(err error, status int) bool {
	statusErr, ok := errors.AsType[*HTTPStatusError](err)
	return ok && statusErr.StatusCode == status
}
