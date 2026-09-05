package onboarding

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"
	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/inventory"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
)

var ErrRevisionConflict = errors.New("configuration changed; read the latest settings and review the update again")
var ErrPolicyDenied = errors.New("configuration management is disabled by the existing policy")

func authorizeSettings(cfg config.Config, tool string) error {
	compiled, err := policy.Compile(cfg, inventory.Static())
	if err != nil {
		return err
	}
	if !compiled.Evaluate(policy.Evaluation{ToolName: tool}).Allowed {
		return ErrPolicyDenied
	}
	return nil
}

// Settings is an allowlisted, credential-free view. It intentionally does not
// expose keychain account names, environment variable names, or state paths.
type Settings struct {
	Revision     string            `json:"revision"`
	Output       config.OutputMode `json:"output"`
	InputEnabled bool              `json:"input_enabled"`
	Devices      []DeviceSettings  `json:"devices"`
}

type DeviceSettings struct {
	DeviceID         string   `json:"device_id"`
	Name             string   `json:"name"`
	Origin           string   `json:"origin"`
	Exposed          bool     `json:"exposed"`
	Permissions      []string `json:"permissions"`
	TakeoverAllowed  bool     `json:"takeover_allowed"`
	IdleTimeout      string   `json:"idle_timeout"`
	AbsoluteLifetime string   `json:"absolute_lifetime"`
}

// SettingsPatch cannot alter stable identities, installer-owned paths, TLS
// pins, credential sources, or the administrative policy that authorizes it.
// Global input is an explicit separate choice, never inferred from a device.
type SettingsPatch struct {
	ExpectedRevision string             `json:"expected_revision"`
	Output           *config.OutputMode `json:"output,omitempty"`
	InputEnabled     *bool              `json:"input_enabled,omitempty"`
	Device           *DevicePatch       `json:"device,omitempty"`
}

type DevicePatch struct {
	DeviceID         string  `json:"device_id"`
	Exposed          *bool   `json:"exposed,omitempty"`
	InputEnabled     *bool   `json:"input_enabled,omitempty"`
	TakeoverAllowed  *bool   `json:"takeover_allowed,omitempty"`
	IdleTimeout      *string `json:"idle_timeout,omitempty"`
	AbsoluteLifetime *string `json:"absolute_lifetime,omitempty"`
}

func (s *Service) Settings() (Settings, error) {
	cfg, data, err := s.readSettings()
	if err != nil {
		return Settings{}, err
	}
	return settingsView(cfg, data), nil
}

func (s *Service) readSettings() (config.Config, []byte, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return config.Config{}, nil, config.ErrMissing
	}
	if err != nil {
		return config.Config{}, nil, err
	}
	var cfg config.Config
	// Decode through the same strict configuration loader without a second
	// filesystem read, binding revision and values to one snapshot.
	cfg, err = config.Decode(bytes.NewReader(data))
	return cfg, data, err
}

func settingsView(cfg config.Config, data []byte) Settings {
	v := Settings{Revision: revision(data), Output: cfg.Output.Default, InputEnabled: inputEnabled(cfg), Devices: []DeviceSettings{}}
	for name, d := range cfg.Devices {
		v.Devices = append(v.Devices, DeviceSettings{DeviceID: d.DeviceID, Name: name, Origin: d.Origin, Exposed: d.Exposed, Permissions: slices.Clone(d.Permissions), TakeoverAllowed: d.Takeover.Allowed, IdleTimeout: d.Session.IdleTimeout.String(), AbsoluteLifetime: d.Session.AbsoluteLifetime.String()})
	}
	slices.SortFunc(v.Devices, func(a, b DeviceSettings) int { return cmp.Compare(a.Name, b.Name) })
	return v
}

func revision(data []byte) string { return config.Revision(data) }
func inputEnabled(cfg config.Config) bool {
	return (len(cfg.Toolsets.Allow) == 0 || slices.Contains(cfg.Toolsets.Allow, "input")) && !slices.Contains(cfg.Toolsets.Deny, "input")
}

// Preview resolves the exact before/after values that the human approves.
func (s *Service) Preview(patch SettingsPatch) (Settings, Settings, error) {
	cfg, data, err := s.readSettings()
	if err != nil {
		return Settings{}, Settings{}, err
	}
	if err := authorizeSettings(cfg, "jetkvm_update_config"); err != nil {
		return Settings{}, Settings{}, err
	}
	before := settingsView(cfg, data)
	if before.Revision != patch.ExpectedRevision {
		return Settings{}, Settings{}, ErrRevisionConflict
	}
	if err := applySettings(&cfg, patch); err != nil {
		return Settings{}, Settings{}, err
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return Settings{}, Settings{}, err
	}
	return before, settingsView(cfg, encoded), nil
}

func (s *Service) Update(ctx context.Context, patch SettingsPatch) (Receipt, error) {
	commit := func() (Receipt, error) { return s.update(ctx, patch) }
	if s.change != nil {
		return s.change(ctx, "jetkvm_update_config", commit)
	}
	return commit()
}

func (s *Service) update(ctx context.Context, patch SettingsPatch) (Receipt, error) {
	lock := flock.New(s.path + ".lock")
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return Receipt{}, err
	}
	if !locked {
		return Receipt{}, ErrConflict
	}
	defer lock.Unlock()
	info, err := os.Lstat(s.path)
	if err != nil {
		return Receipt{}, err
	}
	if !info.Mode().IsRegular() {
		return Receipt{}, ErrConflict
	}
	cfg, data, err := s.readSettings()
	if err != nil {
		return Receipt{}, err
	}
	if revision(data) != patch.ExpectedRevision {
		return Receipt{}, ErrRevisionConflict
	}
	if err := authorizeSettings(cfg, "jetkvm_update_config"); err != nil {
		return Receipt{}, err
	}
	if err := applySettings(&cfg, patch); err != nil {
		return Receipt{}, err
	}
	encoded, err := json.Marshal(cfg, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return Receipt{}, err
	}
	encoded = append(encoded, '\n')
	if err := writeSettings(ctx, s.path, encoded); err != nil {
		return Receipt{}, err
	}
	r := Receipt{Status: "updated", Revision: revision(encoded), Permissions: []string{}}
	if patch.Device != nil {
		for name, d := range cfg.Devices {
			if d.DeviceID == patch.Device.DeviceID {
				r.DeviceID = d.DeviceID
				r.Name = name
				r.Origin = d.Origin
				r.Permissions = slices.Clone(d.Permissions)
			}
		}
	}
	return r, nil
}

func applySettings(cfg *config.Config, patch SettingsPatch) error {
	if patch.ExpectedRevision == "" || patch.Output == nil && patch.InputEnabled == nil && patch.Device == nil {
		return ErrInvalid
	}
	if patch.Output != nil {
		cfg.Output.Default = *patch.Output
	}
	if patch.InputEnabled != nil {
		if *patch.InputEnabled {
			cfg.Toolsets.Deny = slices.DeleteFunc(cfg.Toolsets.Deny, func(s string) bool { return s == "input" })
			if len(cfg.Toolsets.Allow) > 0 && !slices.Contains(cfg.Toolsets.Allow, "input") {
				cfg.Toolsets.Allow = append(cfg.Toolsets.Allow, "input")
			}
		} else {
			if !slices.Contains(cfg.Toolsets.Deny, "input") {
				cfg.Toolsets.Deny = append(cfg.Toolsets.Deny, "input")
			}
		}
	}
	if p := patch.Device; p != nil {
		if p.DeviceID == "" || p.Exposed == nil && p.InputEnabled == nil && p.TakeoverAllowed == nil && p.IdleTimeout == nil && p.AbsoluteLifetime == nil {
			return ErrInvalid
		}
		found := false
		for name, d := range cfg.Devices {
			if d.DeviceID != p.DeviceID {
				continue
			}
			found = true
			if p.Exposed != nil {
				d.Exposed = *p.Exposed
			}
			if p.InputEnabled != nil {
				if *p.InputEnabled {
					if !inputEnabled(*cfg) {
						return ErrConflict
					}
					if !slices.Contains(d.Permissions, "input") {
						d.Permissions = append(d.Permissions, "input")
					}
				} else {
					d.Permissions = slices.DeleteFunc(d.Permissions, func(s string) bool { return s == "input" })
				}
			}
			if p.TakeoverAllowed != nil {
				d.Takeover.Allowed = *p.TakeoverAllowed
			}
			if p.IdleTimeout != nil {
				duration, err := time.ParseDuration(*p.IdleTimeout)
				if err != nil {
					return ErrInvalid
				}
				d.Session.IdleTimeout = config.Duration{Duration: duration}
			}
			if p.AbsoluteLifetime != nil {
				duration, err := time.ParseDuration(*p.AbsoluteLifetime)
				if err != nil {
					return ErrInvalid
				}
				d.Session.AbsoluteLifetime = config.Duration{Duration: duration}
			}
			cfg.Devices[name] = d
		}
		if !found {
			return ErrConflict
		}
	}
	if err := cfg.Validate(); err != nil {
		return errors.Join(ErrInvalid, err)
	}
	return nil
}

func writeSettings(ctx context.Context, path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".jetkvm-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	if directory, openErr := os.Open(filepath.Dir(path)); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
