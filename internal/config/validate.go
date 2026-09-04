package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var knownToolsets = []string{"observe", "video", "input", "power", "media"}

func (c Config) Validate() error {
	var errs []error
	if c.Version != CurrentVersion {
		errs = append(errs, fmt.Errorf("version must be %d", CurrentVersion))
	}
	errs = append(errs, c.Transport.validate(), c.Output.validate())
	if c.State.Path == "" || !filepath.IsAbs(c.State.Path) {
		errs = append(errs, errors.New("state.path must be an absolute path"))
	}
	errs = append(errs, validateSelection("toolsets", c.Toolsets, knownToolsets))
	errs = append(errs, validateSelection("tools", c.Tools, nil))
	for alias, device := range c.Devices {
		errs = append(errs, device.validate(alias))
	}
	errs = append(errs, c.Retention.validate())
	return errors.Join(errs...)
}

func (c TransportConfig) validate() error {
	switch c.Type {
	case TransportStdio:
		if c.HTTP != nil {
			return errors.New("transport.http is only valid for streamable_http")
		}
	case TransportStreamableHTTP:
		if c.HTTP == nil {
			return errors.New("transport.http is required for streamable_http")
		}
		host, _, err := net.SplitHostPort(c.HTTP.Listen)
		if err != nil {
			return fmt.Errorf("transport.http.listen must be host:port: %w", err)
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("transport.http.listen must use a numeric loopback address")
		}
	default:
		return fmt.Errorf("unsupported transport.type %q", c.Type)
	}
	return nil
}

func (c OutputConfig) validate() error {
	if c.Default != OutputAuto && c.Default != OutputJSON && c.Default != OutputText {
		return fmt.Errorf("unsupported output.default %q", c.Default)
	}
	return nil
}

func (c DeviceConfig) validate(alias string) error {
	var errs []error
	if strings.TrimSpace(alias) == "" {
		errs = append(errs, errors.New("device alias must not be empty"))
	}
	if c.DeviceID == "" {
		errs = append(errs, fmt.Errorf("device %q: device_id is required", alias))
	}
	origin, err := url.Parse(c.Origin)
	if err != nil {
		errs = append(errs, fmt.Errorf("device %q: parse origin: %w", alias, err))
	} else if err := validateOrigin(origin, c.AllowPlainHTTP); err != nil {
		errs = append(errs, fmt.Errorf("device %q: %w", alias, err))
	}
	errs = append(errs,
		validateSelection("device "+alias+" permissions", Selection{Allow: c.Permissions}, knownToolsets),
		c.Credentials.validate(alias),
		c.TLS.validate(alias, origin),
		c.Session.validate(alias),
	)
	return errors.Join(errs...)
}

func validateOrigin(origin *url.URL, allowPlainHTTP bool) error {
	if origin.Scheme != "http" && origin.Scheme != "https" {
		return errors.New("origin scheme must be http or https")
	}
	if origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("origin must be an exact authority without userinfo, query, or fragment")
	}
	if origin.Path != "" && origin.Path != "/" {
		return errors.New("origin path must be empty or root")
	}
	if origin.Scheme == "http" && !allowPlainHTTP {
		return errors.New("plain HTTP requires allow_plain_http=true")
	}
	return nil
}

func (c CredentialConfig) validate(alias string) error {
	switch c.Provider {
	case CredentialKeychain:
		if c.Service == "" || c.Account == "" || c.Variable != "" {
			return fmt.Errorf("device %q: keychain credentials require service and account only", alias)
		}
	case CredentialEnvironment:
		if c.Variable == "" || c.Service != "" || c.Account != "" {
			return fmt.Errorf("device %q: environment credentials require variable only", alias)
		}
	case CredentialNoPassword:
		if c.Service != "" || c.Account != "" || c.Variable != "" {
			return fmt.Errorf("device %q: no_password credentials accept no additional fields", alias)
		}
	default:
		return fmt.Errorf("device %q: unsupported credential provider %q", alias, c.Provider)
	}
	return nil
}

func (c TLSConfig) validate(alias string, origin *url.URL) error {
	if origin == nil || origin.Scheme != "https" {
		if c.Mode != "" || c.SPKISHA256 != "" {
			return fmt.Errorf("device %q: tls configuration is only valid for HTTPS", alias)
		}
		return nil
	}
	switch c.Mode {
	case TLSSystem:
		if c.SPKISHA256 != "" {
			return fmt.Errorf("device %q: spki_sha256 requires pinned TLS mode", alias)
		}
	case TLSPinned:
		if c.SPKISHA256 == "" {
			return fmt.Errorf("device %q: pinned TLS mode requires spki_sha256", alias)
		}
		decoded, err := hex.DecodeString(c.SPKISHA256)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("device %q: spki_sha256 must be a 64-character hexadecimal SHA-256 digest", alias)
		}
	default:
		return fmt.Errorf("device %q: unsupported tls.mode %q", alias, c.Mode)
	}
	return nil
}

func (c SessionConfig) validate(alias string) error {
	if c.IdleTimeout.Duration <= 0 {
		return fmt.Errorf("device %q: session.idle_timeout must be positive", alias)
	}
	if c.AbsoluteLifetime.Duration <= 0 {
		return fmt.Errorf("device %q: session.absolute_lifetime must be positive", alias)
	}
	if c.IdleTimeout.Duration > c.AbsoluteLifetime.Duration {
		return fmt.Errorf("device %q: session.idle_timeout must not exceed absolute_lifetime", alias)
	}
	return nil
}

func (c RetentionConfig) validate() error {
	values := []struct {
		name  string
		value time.Duration
	}{
		{"operation_receipts", c.OperationReceipts.Duration},
		{"security_audit", c.SecurityAudit.Duration},
		{"observation_metadata", c.ObservationMetadata.Duration},
		{"screenshots", c.Screenshots.Duration},
	}
	var errs []error
	for _, item := range values {
		if item.value < 0 {
			errs = append(errs, fmt.Errorf("retention.%s must not be negative", item.name))
		}
	}
	return errors.Join(errs...)
}

func validateSelection(name string, selection Selection, known []string) error {
	var errs []error
	for kind, values := range map[string][]string{"allow": selection.Allow, "deny": selection.Deny} {
		seen := make(map[string]struct{})
		for _, value := range values {
			if value == "" {
				errs = append(errs, fmt.Errorf("%s.%s contains an empty value", name, kind))
				continue
			}
			if known != nil && !slices.Contains(known, value) {
				errs = append(errs, fmt.Errorf("%s.%s contains unknown value %q", name, kind, value))
			}
			if _, ok := seen[value]; ok {
				errs = append(errs, fmt.Errorf("%s.%s contains duplicate value %q", name, kind, value))
			} else {
				seen[value] = struct{}{}
			}
		}
	}
	return errors.Join(errs...)
}
