// Package onboarding owns device enrollment for humans and agents. Device
// identities are observed, never supplied as authorization identities by callers.
package onboarding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"uuid"

	"github.com/gofrs/flock"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/progress"
	"github.com/zalando/go-keyring"
)

var (
	ErrPasswordRequired = errors.New("this device requires a password")
	ErrAuthentication   = errors.New("the device did not accept the password")
	ErrConflict         = errors.New("the device or its name is already configured differently")
	ErrUnavailable      = errors.New("could not connect to the device; check its address and network")
	ErrCredentialStore  = errors.New("could not safely save the device credential")
	ErrInvalid          = errors.New("invalid device setup request")
	ErrActiveControls   = errors.New("close the current control sessions before connecting another device")
	ErrActivation       = errors.New("device setup was saved but could not be activated; retry setup to activate the saved device")
)

type Request struct {
	Address   string `json:"address"`
	Name      string `json:"name,omitempty"`
	AllowHTTP bool   `json:"allow_http"`
	Control   bool   `json:"control"`
}

// Secret never appears in a tool schema, result, configuration file, or log.
type Secret struct{ Password []byte }

func (Secret) MarshalJSON() ([]byte, error) {
	return nil, errors.New("credentials cannot be serialized")
}
func (Secret) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "onboarding.Secret{REDACTED}") }

type Receipt struct {
	Status      string   `json:"status"`
	DeviceID    string   `json:"device_id"`
	Name        string   `json:"name"`
	Origin      string   `json:"origin"`
	Revision    string   `json:"revision"`
	Permissions []string `json:"permissions"`
}

type Keyring interface {
	Get(string, string) (string, error)
	Set(string, string, string) error
	Delete(string, string) error
}
type systemKeyring struct{}

func (systemKeyring) Get(s, a string) (string, error) { return keyring.Get(s, a) }
func (systemKeyring) Set(s, a, p string) error        { return keyring.Set(s, a, p) }
func (systemKeyring) Delete(s, a string) error        { return keyring.Delete(s, a) }

type Options struct {
	Path    string
	Keyring Keyring
	// Change holds the application's reconfiguration boundary while commit
	// runs, then reloads the committed configuration before releasing it.
	Change func(context.Context, string, func() (Receipt, error)) (Receipt, error)
}
type Service struct {
	path    string
	keyring Keyring
	change  func(context.Context, string, func() (Receipt, error)) (Receipt, error)
}

func New(options Options) (*Service, error) {
	if strings.TrimSpace(options.Path) == "" {
		return nil, ErrInvalid
	}
	path, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, ErrInvalid
	}
	keys := options.Keyring
	if keys == nil {
		keys = systemKeyring{}
	}
	return &Service{path: path, keyring: keys, change: options.Change}, nil
}

func NormalizeAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" || len(address) > 2048 {
		return "", ErrInvalid
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	origin, err := jetkvm.CanonicalOrigin(address)
	if err != nil {
		return "", ErrInvalid
	}
	return origin, nil
}

func (s *Service) Needed() (bool, error) {
	cfg, err := config.Load(s.path)
	if errors.Is(err, config.ErrMissing) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(cfg.Devices) == 0, nil
}

func (s *Service) Connect(ctx context.Context, request Request, secret Secret) (Receipt, error) {
	origin, err := NormalizeAddress(request.Address)
	if err != nil {
		return Receipt{}, err
	}
	if strings.HasPrefix(origin, "http://") && !request.AllowHTTP {
		return Receipt{}, jetkvm.ErrPlainHTTPNotAllowed
	}
	if len(request.Name) > 80 || strings.ContainsFunc(request.Name, unicode.IsControl) {
		return Receipt{}, ErrInvalid
	}
	if len(secret.Password) > 4096 {
		return Receipt{}, ErrInvalid
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var provider jetkvm.CredentialProvider
	if len(secret.Password) > 0 {
		provider = jetkvm.CredentialProviderFunc(func(context.Context) ([]byte, error) { return bytes.Clone(secret.Password), nil })
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client, err := jetkvm.NewClient(jetkvm.Config{Origin: origin, AllowPlainHTTP: request.AllowHTTP, Credentials: provider, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		return Receipt{}, ErrInvalid
	}
	progress.Stage(ctx, "Verifying device connection")
	device, err := client.GetDevice(probeCtx)
	if err != nil {
		if errors.Is(err, jetkvm.ErrCredentialUnavailable) {
			return Receipt{}, ErrPasswordRequired
		}
		if errors.Is(err, jetkvm.ErrAuthenticationFailed) {
			return Receipt{}, ErrAuthentication
		}
		if ctx.Err() != nil {
			return Receipt{}, context.Cause(ctx)
		}
		return Receipt{}, ErrUnavailable
	}
	if device.DeviceID == "" || len(device.DeviceID) > 256 || strings.ContainsFunc(device.DeviceID, unicode.IsControl) {
		return Receipt{}, ErrUnavailable
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		digest := sha256.Sum256([]byte(device.DeviceID))
		name = fmt.Sprintf("jetkvm-%x", digest[:3])
	}
	commit := func() (Receipt, error) { return s.save(ctx, request, origin, name, device, secret) }
	if s.change != nil {
		return s.change(ctx, "jetkvm_setup", commit)
	}
	return commit()
}

func (s *Service) save(ctx context.Context, request Request, origin, name string, device jetkvm.LocalDevice, secret Secret) (receipt Receipt, err error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return receipt, err
	}
	lock := flock.New(s.path + ".lock")
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return receipt, err
	}
	if !locked {
		return receipt, ErrConflict
	}
	defer lock.Unlock()
	if info, err := os.Lstat(s.path); err == nil && !info.Mode().IsRegular() {
		return receipt, ErrConflict
	}
	cfg, err := config.Load(s.path)
	newConfig := errors.Is(err, config.ErrMissing)
	if errors.Is(err, config.ErrMissing) {
		cfg = config.Default()
		cfg.State.Path = filepath.Join(filepath.Dir(s.path), "state.db")
	} else if err != nil {
		return receipt, err
	}
	if err := authorizeSettings(cfg, "jetkvm_setup"); err != nil {
		return receipt, err
	}
	for alias, existing := range cfg.Devices {
		if existing.DeviceID == device.DeviceID {
			if existing.Origin != origin || request.Name != "" && alias != name || !existing.Exposed || request.Control && !slices.Contains(existing.Permissions, "input") {
				return receipt, ErrConflict
			}
			return s.receipt("already_configured", alias, existing)
		}
		if alias == name || existing.Origin == origin {
			return receipt, ErrConflict
		}
	}
	if request.Control && !newConfig && (!slices.Contains(cfg.Toolsets.Allow, "input") && len(cfg.Toolsets.Allow) > 0 || slices.Contains(cfg.Toolsets.Deny, "input")) {
		return receipt, ErrConflict
	}
	credential := config.CredentialConfig{Provider: config.CredentialNoPassword}
	createdCredential := false
	if device.AuthMode == "password" {
		if len(secret.Password) == 0 {
			return receipt, ErrPasswordRequired
		}
		account := uuid.NewV7().String()
		credential = config.CredentialConfig{Provider: config.CredentialKeychain, Service: "jetkvm.device", Account: account}
		if _, getErr := s.keyring.Get(credential.Service, account); !errors.Is(getErr, keyring.ErrNotFound) {
			return receipt, ErrCredentialStore
		}
		if s.keyring.Set(credential.Service, account, string(secret.Password)) != nil {
			return receipt, ErrCredentialStore
		}
		createdCredential = true
		defer func() {
			if err != nil && createdCredential {
				if s.keyring.Delete(credential.Service, account) != nil {
					err = errors.Join(err, ErrCredentialStore)
				}
			}
		}()
	}
	permissions := []string{"observe", "video"}
	if request.Control {
		permissions = append(permissions, "input")
	}
	// Preserve the operator's existing deployment ceiling. New configurations
	// may enable input only through an explicit setup choice.
	if request.Control && newConfig {
		cfg.Toolsets.Allow = append(cfg.Toolsets.Allow, "input")
	}
	mode := config.TLSMode("")
	if strings.HasPrefix(origin, "https://") {
		mode = config.TLSSystem
	}
	dc := config.DeviceConfig{
		DeviceID: device.DeviceID, Origin: origin, Exposed: true,
		AllowPlainHTTP: request.AllowHTTP, Credentials: credential,
		TLS: config.TLSConfig{Mode: mode}, Permissions: permissions,
		Takeover: config.TakeoverConfig{Allowed: true, RequireConfirmation: true},
		Session: config.SessionConfig{
			IdleTimeout:      config.Duration{Duration: 5 * time.Minute},
			AbsoluteLifetime: config.Duration{Duration: 30 * time.Minute},
		},
	}
	cfg.Devices[name] = dc
	if err = cfg.Validate(); err != nil {
		return receipt, err
	}
	data, err := json.Marshal(cfg, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return receipt, err
	}
	progress.Stage(ctx, "Saving device setup")
	if err = writeSettings(ctx, s.path, append(data, '\n')); err != nil {
		return receipt, err
	}
	// The config now owns the credential. Never delete it on a later readback
	// or activation error, because that would break a committed enrollment.
	createdCredential = false
	return s.receipt("connected", name, dc)
}

func (s *Service) receipt(status, name string, dc config.DeviceConfig) (Receipt, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Status: status, DeviceID: dc.DeviceID, Name: name, Origin: dc.Origin, Revision: config.Revision(data), Permissions: slices.Clone(dc.Permissions)}, nil
}
