package config

import (
	"strings"
	"testing"
	"time"
)

const validConfig = `{
  "version": 1,
  "transport": {"type": "stdio"},
  "output": {"default": "json"},
  "state": {"path": "/var/lib/jetkvm/state.db"},
  "toolsets": {"allow": ["observe", "video", "input"], "deny": []},
  "tools": {"allow": [], "deny": ["jetkvm_open_control"]},
  "devices": {
    "lab": {
      "device_id": "device-1",
      "origin": "http://192.0.2.1",
      "exposed": true,
      "allow_plain_http": true,
      "credentials": {"provider": "no_password"},
      "tls": {"mode": ""},
      "permissions": ["observe", "video", "input"],
      "takeover": {"allowed": true, "require_confirmation": true},
      "session": {"idle_timeout": "5m", "absolute_lifetime": "30m"}
    }
  },
  "retention": {
    "operation_receipts": "720h",
    "security_audit": "2160h",
    "observation_metadata": "24h",
    "screenshots": "0s"
  }
}`

func TestDecodeStrictAndDefaults(t *testing.T) {
	configuration, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Output.Default != OutputJSON {
		t.Fatalf("output default = %q", configuration.Output.Default)
	}
	if got := configuration.Devices["lab"].Session.IdleTimeout.Duration; got != 5*time.Minute {
		t.Fatalf("idle timeout = %v", got)
	}
}

func TestDecodeAppliesSessionDefaults(t *testing.T) {
	input := strings.Replace(validConfig, `"session": {"idle_timeout": "5m", "absolute_lifetime": "30m"}`, `"session": {}`, 1)
	configuration, err := Decode(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	session := configuration.Devices["lab"].Session
	if session.IdleTimeout.Duration != 5*time.Minute || session.AbsoluteLifetime.Duration != 30*time.Minute {
		t.Fatalf("session defaults = %+v", session)
	}
}

func TestDecodeRejectsUnknownAndDuplicateMembers(t *testing.T) {
	for name, input := range map[string]string{
		"unknown top-level": strings.Replace(validConfig, `"version": 1,`, `"version": 1, "surprise": true,`, 1),
		"unknown nested":    strings.Replace(validConfig, `"device_id": "device-1",`, `"device_id": "device-1", "password": "secret",`, 1),
		"duplicate":         strings.Replace(validConfig, `"version": 1,`, `"version": 1, "version": 1,`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input)); err == nil {
				t.Fatal("Decode succeeded")
			}
		})
	}
}

func TestValidateRejectsCredentialAndRetentionErrors(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"credential fields": func(c *Config) {
			device := c.Devices["lab"]
			device.Credentials = CredentialConfig{Provider: CredentialEnvironment, Variable: "JETKVM_PASSWORD", Account: "not-allowed"}
			c.Devices["lab"] = device
		},
		"negative retention": func(c *Config) {
			c.Retention.SecurityAudit.Duration = -time.Second
		},
		"invalid output": func(c *Config) {
			c.Output.Default = "xml"
		},
		"relative state path": func(c *Config) {
			c.State.Path = "state.db"
		},
		"malformed SPKI pin": func(c *Config) {
			device := c.Devices["lab"]
			device.Origin = "https://jetkvm.example"
			device.AllowPlainHTTP = false
			device.TLS = TLSConfig{Mode: TLSPinned, SPKISHA256: "short"}
			c.Devices["lab"] = device
		},
	} {
		t.Run(name, func(t *testing.T) {
			configuration, err := Decode(strings.NewReader(validConfig))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&configuration)
			if err := configuration.Validate(); err == nil {
				t.Fatal("Validate succeeded")
			}
		})
	}
}

func TestSelectionAllowsOverlapBecauseDenyWins(t *testing.T) {
	configuration, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	configuration.Toolsets.Deny = []string{"video"}
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOriginAndLoopbackHTTP(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"origin path": func(c *Config) {
			device := c.Devices["lab"]
			device.Origin = "http://192.0.2.1/device"
			c.Devices["lab"] = device
		},
		"plain HTTP opt-in": func(c *Config) {
			device := c.Devices["lab"]
			device.AllowPlainHTTP = false
			c.Devices["lab"] = device
		},
		"non-loopback listener": func(c *Config) {
			c.Transport = TransportConfig{Type: TransportStreamableHTTP, HTTP: &HTTPConfig{Listen: "0.0.0.0:8080"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			configuration, err := Decode(strings.NewReader(validConfig))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&configuration)
			if err := configuration.Validate(); err == nil {
				t.Fatal("Validate succeeded")
			}
		})
	}
}

func TestNarrowCannotExpandDeploymentCeiling(t *testing.T) {
	configuration, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configuration.Narrow(ProcessConstraints{ToolsetsAllow: []string{"power"}}); err == nil {
		t.Fatal("Narrow allowed toolset expansion")
	}
	if _, err := configuration.Narrow(ProcessConstraints{DevicesAllow: []string{"unknown"}}); err == nil {
		t.Fatal("Narrow allowed device expansion")
	}
	if _, err := configuration.Narrow(ProcessConstraints{DevicePermissions: map[string][]string{"lab": {"power"}}}); err == nil {
		t.Fatal("Narrow allowed device permission expansion")
	}
}

func TestNarrowIntersectsAllowAndUnionsDeny(t *testing.T) {
	configuration, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	mode := OutputText
	narrowed, err := configuration.Narrow(ProcessConstraints{
		Output:            &mode,
		ToolsetsAllow:     []string{"observe", "input"},
		ToolsetsDeny:      []string{"input"},
		DevicePermissions: map[string][]string{"lab": {"observe"}},
		DisableTakeover:   []string{"lab"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if narrowed.Output.Default != OutputText || len(narrowed.Toolsets.Allow) != 2 || len(narrowed.Toolsets.Deny) != 1 {
		t.Fatalf("unexpected narrowed config: %+v", narrowed)
	}
	device := narrowed.Devices["lab"]
	if device.Takeover.Allowed || len(device.Permissions) != 1 || device.Permissions[0] != "observe" {
		t.Fatalf("unexpected narrowed device: %+v", device)
	}
	if !configuration.Devices["lab"].Takeover.Allowed || len(configuration.Devices["lab"].Permissions) != 3 {
		t.Fatal("Narrow mutated source config")
	}
}

func TestNarrowSelectsFromUnrestrictedToolCeiling(t *testing.T) {
	configuration, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	narrowed, err := configuration.Narrow(ProcessConstraints{ToolsAllow: []string{"jetkvm_get_status"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed.Tools.Allow) != 1 || narrowed.Tools.Allow[0] != "jetkvm_get_status" {
		t.Fatalf("tools allow = %v", narrowed.Tools.Allow)
	}
}

func TestConfirmationDefaultsAndStrictDecode(t *testing.T) {
	for _, value := range []string{"", `"confirmation":{"required":false},`, `"confirmation":{"required":true},`} {
		input := strings.Replace(validConfig, `"version": 1,`, `"version": 1,`+value, 1)
		cfg, err := Decode(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Confirmation.Required != strings.Contains(value, "true") {
			t.Fatalf("confirmation %q: %+v", value, cfg.Confirmation)
		}
	}
	for _, value := range []string{`{"required":"false"}`, `{"required":false,"allow_all":true}`} {
		input := strings.Replace(validConfig, `"version": 1,`, `"version": 1,"confirmation":`+value+`,`, 1)
		if _, err := Decode(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid confirmation %s", value)
		}
	}
}
