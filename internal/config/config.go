package config

import (
	"encoding/json/v2"
	"time"
)

const CurrentVersion = 1

type Config struct {
	Version   int                     `json:"version"`
	Transport TransportConfig         `json:"transport"`
	Output    OutputConfig            `json:"output"`
	State     StateConfig             `json:"state"`
	Toolsets  Selection               `json:"toolsets"`
	Tools     Selection               `json:"tools"`
	Devices   map[string]DeviceConfig `json:"devices"`
	Retention RetentionConfig         `json:"retention"`
}

type Selection struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type TransportType string

const (
	TransportStdio          TransportType = "stdio"
	TransportStreamableHTTP TransportType = "streamable_http"
)

type TransportConfig struct {
	Type TransportType `json:"type"`
	HTTP *HTTPConfig   `json:"http,omitempty"`
}

type HTTPConfig struct {
	Listen string `json:"listen"`
}

type OutputMode string

const (
	OutputAuto OutputMode = "auto"
	OutputJSON OutputMode = "json"
	OutputText OutputMode = "text"
)

type OutputConfig struct {
	Default OutputMode `json:"default"`
}

type StateConfig struct {
	Path string `json:"path"`
}

type DeviceConfig struct {
	DeviceID       string            `json:"device_id"`
	Origin         string            `json:"origin"`
	Exposed        bool              `json:"exposed"`
	AllowPlainHTTP bool              `json:"allow_plain_http,omitzero"`
	Labels         map[string]string `json:"labels,omitempty"`
	Credentials    CredentialConfig  `json:"credentials"`
	TLS            TLSConfig         `json:"tls"`
	Permissions    []string          `json:"permissions"`
	Takeover       TakeoverConfig    `json:"takeover"`
	Session        SessionConfig     `json:"session"`
}

func (c *DeviceConfig) UnmarshalJSON(data []byte) error {
	type plain DeviceConfig
	value := plain{
		Session: SessionConfig{
			IdleTimeout:      Duration{5 * time.Minute},
			AbsoluteLifetime: Duration{30 * time.Minute},
		},
	}
	if err := json.Unmarshal(data, &value, json.RejectUnknownMembers(true)); err != nil {
		return err
	}
	*c = DeviceConfig(value)
	return nil
}

type CredentialProvider string

const (
	CredentialKeychain    CredentialProvider = "keychain"
	CredentialEnvironment CredentialProvider = "environment"
	CredentialNoPassword  CredentialProvider = "no_password"
)

type CredentialConfig struct {
	Provider CredentialProvider `json:"provider"`
	Service  string             `json:"service,omitempty"`
	Account  string             `json:"account,omitempty"`
	Variable string             `json:"variable,omitempty"`
}

type TLSMode string

const (
	TLSSystem TLSMode = "system"
	TLSPinned TLSMode = "pinned"
)

type TLSConfig struct {
	Mode       TLSMode `json:"mode"`
	SPKISHA256 string  `json:"spki_sha256,omitempty"`
}

type TakeoverConfig struct {
	Allowed             bool `json:"allowed"`
	RequireConfirmation bool `json:"require_confirmation"`
}

type SessionConfig struct {
	IdleTimeout      Duration `json:"idle_timeout"`
	AbsoluteLifetime Duration `json:"absolute_lifetime"`
}

type RetentionConfig struct {
	OperationReceipts   Duration `json:"operation_receipts"`
	SecurityAudit       Duration `json:"security_audit"`
	ObservationMetadata Duration `json:"observation_metadata"`
	Screenshots         Duration `json:"screenshots"`
}

func Default() Config {
	return Config{
		Version:   CurrentVersion,
		Transport: TransportConfig{Type: TransportStdio},
		Output:    OutputConfig{Default: OutputAuto},
		Toolsets:  Selection{Allow: []string{"observe", "video"}},
		Devices:   make(map[string]DeviceConfig),
		Retention: RetentionConfig{
			OperationReceipts:   Duration{30 * 24 * time.Hour},
			SecurityAudit:       Duration{90 * 24 * time.Hour},
			ObservationMetadata: Duration{24 * time.Hour},
		},
	}
}
