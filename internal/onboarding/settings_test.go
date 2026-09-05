package onboarding

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/config"
)

func configuredSettings(t *testing.T) (*Service, string) {
	t.Helper()
	device := fixtureDevice(t, "settings-fixture", "")
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connect(t.Context(), Request{Address: device.URL, AllowHTTP: true}, Secret{}); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestSettingsUpdateIsRevisionBoundAndPreservesOtherFields(t *testing.T) {
	t.Parallel()
	s, path := configuredSettings(t)
	settings, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := config.Load(path)
	patch := SettingsPatch{ExpectedRevision: settings.Revision, Output: new(config.OutputText), InputEnabled: new(true), Device: &DevicePatch{DeviceID: "settings-fixture", InputEnabled: new(true), IdleTimeout: new("3m")}}
	old, next, err := s.Preview(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(SettingChanges(old, next)) != 4 {
		t.Fatalf("changes: %+v", SettingChanges(old, next))
	}
	unchanged, _ := s.Settings()
	if unchanged.Revision != settings.Revision {
		t.Fatal("preview wrote configuration")
	}
	receipt, err := s.Update(t.Context(), patch)
	if err != nil || receipt.Status != "updated" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, original := range before.Devices {
		changed := after.Devices[name]
		if changed.DeviceID != original.DeviceID || changed.Origin != original.Origin || changed.Credentials != original.Credentials || changed.Takeover.RequireConfirmation != original.Takeover.RequireConfirmation || !slices.Contains(changed.Permissions, "input") {
			t.Fatalf("unsafe update: %+v", changed)
		}
	}
	if after.State != before.State || after.Retention != before.Retention {
		t.Fatal("unrelated configuration changed")
	}
	if _, err := s.Update(t.Context(), patch); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale patch applied: %v", err)
	}
	current, _ := s.Settings()
	if current.Revision != receipt.Revision {
		t.Fatal("receipt revision not authoritative")
	}
}

func TestSettingsRejectsInvalidOrImplicitPrivilegeChanges(t *testing.T) {
	t.Parallel()
	s, path := configuredSettings(t)
	settings, _ := s.Settings()
	for _, patch := range []SettingsPatch{
		{ExpectedRevision: settings.Revision},
		{ExpectedRevision: settings.Revision, Device: &DevicePatch{DeviceID: "settings-fixture", InputEnabled: new(true)}},
		{ExpectedRevision: settings.Revision, Device: &DevicePatch{DeviceID: "unknown", Exposed: new(false)}},
		{ExpectedRevision: settings.Revision, Device: &DevicePatch{DeviceID: "settings-fixture", IdleTimeout: new("-1m")}},
		{ExpectedRevision: settings.Revision, Output: new(config.OutputMode("foreign"))},
	} {
		if _, err := s.Update(t.Context(), patch); err == nil {
			t.Fatalf("invalid patch applied: %+v", patch)
		}
	}
	data, _ := os.ReadFile(path)
	if revision(data) != settings.Revision {
		t.Fatal("rejected change mutated config")
	}
}

func TestConfirmationSettingRoundTripPreservesPermissionsAndRevision(t *testing.T) {
	t.Parallel()
	s, path := configuredSettings(t)
	original, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Settings()
	if err != nil || initial.ConfirmationRequired {
		t.Fatalf("initial confirmation: %+v, %v", initial, err)
	}
	for _, required := range []bool{true, false} {
		current, err := s.Settings()
		if err != nil {
			t.Fatal(err)
		}
		patch := SettingsPatch{ExpectedRevision: current.Revision, ConfirmationRequired: new(required)}
		before, after, err := s.Preview(patch)
		if err != nil || after.ConfirmationRequired != required {
			t.Fatalf("preview: %+v, %v", after, err)
		}
		changes := SettingChanges(before, after)
		if len(changes) != 1 || changes[0].Field != "Require secondary device-action confirmation" {
			t.Fatalf("changes: %+v", changes)
		}
		if _, err := s.Update(t.Context(), patch); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update(t.Context(), patch); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("stale confirmation patch: %v", err)
		}
		stored, err := config.Load(path)
		if err != nil || stored.Confirmation.Required != required {
			t.Fatalf("stored confirmation: %+v, %v", stored.Confirmation, err)
		}
		for name, device := range original.Devices {
			got := stored.Devices[name]
			if !slices.Equal(got.Permissions, device.Permissions) || got.Takeover != device.Takeover || got.DeviceID != device.DeviceID || got.Credentials != device.Credentials {
				t.Fatal("confirmation change altered device authority")
			}
		}
		if !slices.Equal(stored.Toolsets.Allow, original.Toolsets.Allow) || !slices.Equal(stored.Toolsets.Deny, original.Toolsets.Deny) {
			t.Fatal("confirmation change altered tool permissions")
		}
	}
}
