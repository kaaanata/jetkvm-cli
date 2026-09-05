package policy

import (
	"strings"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/inventory"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	configuration, err := config.Decode(strings.NewReader(`{
      "version": 1,
      "transport": {"type": "stdio"},
      "state": {"path": "/state.db"},
      "toolsets": {"allow": ["observe", "video", "input", "power"], "deny": ["power"]},
      "tools": {"allow": [], "deny": ["jetkvm_type_text"]},
      "devices": {
        "lab": {
          "device_id": "stable-1", "origin": "http://192.0.2.1", "exposed": true,
          "allow_plain_http": true, "credentials": {"provider": "no_password"}, "tls": {"mode": ""},
          "permissions": ["observe", "video", "input", "power"],
          "takeover": {"allowed": true, "require_confirmation": true},
          "session": {"idle_timeout": "5m", "absolute_lifetime": "30m"}
        },
        "hidden": {
          "device_id": "stable-2", "origin": "https://example.test", "exposed": false,
          "credentials": {"provider": "keychain", "service": "jetkvm", "account": "hidden"},
          "tls": {"mode": "system"}, "permissions": ["observe"],
          "takeover": {"allowed": false, "require_confirmation": true},
          "session": {"idle_timeout": "5m", "absolute_lifetime": "30m"}
        }
      },
      "retention": {"operation_receipts": "720h", "security_audit": "2160h", "observation_metadata": "24h", "screenshots": "0s"}
    }`))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func TestDenyWinsAtEveryEntryPoint(t *testing.T) {
	compiled, err := Compile(testConfig(t), inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	for _, evaluation := range []Evaluation{
		{ToolName: "jetkvm_power_action", DeviceID: "stable-1"},
		{ToolName: "jetkvm_type_text", DeviceID: "stable-1"},
	} {
		decision := compiled.Evaluate(evaluation)
		if decision.Allowed || decision.Reason != DeniedDeploymentCeiling {
			t.Fatalf("unexpected decision for %s: %+v", evaluation.ToolName, decision)
		}
	}
	for _, tool := range compiled.Tools(Scope{}, "stable-1") {
		if tool.Name == "jetkvm_power_action" || tool.Name == "jetkvm_type_text" {
			t.Fatalf("denied tool appeared in discovery: %s", tool.Name)
		}
	}
}

func TestStableIdentityAndExposureAreRequired(t *testing.T) {
	compiled, err := Compile(testConfig(t), inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	for _, deviceID := range []string{"lab", "stable-2", "missing"} {
		decision := compiled.Evaluate(Evaluation{ToolName: "jetkvm_get_status", DeviceID: deviceID})
		if decision.Allowed || decision.Reason != DeniedDeviceNotExposed {
			t.Fatalf("device %q unexpectedly allowed: %+v", deviceID, decision)
		}
	}
	if decision := compiled.Evaluate(Evaluation{ToolName: "jetkvm_get_status", DeviceID: "stable-1"}); !decision.Allowed {
		t.Fatalf("stable identity denied: %+v", decision)
	}
}

func TestRequestScopeCanOnlyReduce(t *testing.T) {
	compiled, err := Compile(testConfig(t), inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	decision := compiled.Evaluate(Evaluation{
		ToolName: "jetkvm_key_press", DeviceID: "stable-1",
		Scope: Scope{ToolsetsDeny: []string{"input"}},
	})
	if decision.Allowed || decision.Reason != DeniedRequestScope {
		t.Fatalf("request deny was not enforced: %+v", decision)
	}
	decision = compiled.Evaluate(Evaluation{
		ToolName: "jetkvm_power_action", DeviceID: "stable-1",
		Scope: Scope{ToolsetsAllow: []string{"power"}},
	})
	if decision.Allowed || decision.Reason != DeniedDeploymentCeiling {
		t.Fatalf("request scope expanded deployment: %+v", decision)
	}
}

func TestCapabilityGateOnlyAppliesAtExecution(t *testing.T) {
	compiled, err := Compile(testConfig(t), inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	discovery := compiled.Evaluate(Evaluation{ToolName: "jetkvm_key_press", DeviceID: "stable-1"})
	if !discovery.Allowed {
		t.Fatalf("stable discovery denied: %+v", discovery)
	}
	execution := compiled.Evaluate(Evaluation{
		ToolName: "jetkvm_key_press", DeviceID: "stable-1", CheckCapabilities: true,
		Capabilities: map[string]bool{"hid": false},
	})
	if execution.Allowed || execution.Reason != DeniedCapabilityUnavailable {
		t.Fatalf("missing capability allowed: %+v", execution)
	}
}

func TestRevisionIsDeterministic(t *testing.T) {
	configuration := testConfig(t)
	first, err := Compile(configuration, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(configuration, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision() != second.Revision() || !strings.HasPrefix(first.Revision(), "sha256:") {
		t.Fatalf("revisions differ: %q %q", first.Revision(), second.Revision())
	}
}

func TestCompileRejectsUnknownConfiguredTool(t *testing.T) {
	configuration := testConfig(t)
	configuration.Tools.Allow = []string{"jetkvm_does_not_exist"}
	if _, err := Compile(configuration, inventory.Static()); err == nil {
		t.Fatal("Compile succeeded")
	}
}

func TestCompileRejectsDuplicateExposedIdentity(t *testing.T) {
	configuration := testConfig(t)
	duplicate := configuration.Devices["lab"]
	configuration.Devices["second"] = duplicate
	if _, err := Compile(configuration, inventory.Static()); err == nil {
		t.Fatal("Compile succeeded")
	}
}

func TestConfirmationSwitchChangesRevisionWithoutChangingPermissions(t *testing.T) {
	cfg := testConfig(t)
	disabled, err := Compile(cfg, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Confirmation.Required = true
	enabled, err := Compile(cfg, inventory.Static())
	if err != nil {
		t.Fatal(err)
	}
	if disabled.ConfirmationRequired() || !enabled.ConfirmationRequired() || disabled.Revision() == enabled.Revision() {
		t.Fatal("confirmation setting was not compiled into policy revision")
	}
	for _, compiled := range []*Compiled{disabled, enabled} {
		decision := compiled.Evaluate(Evaluation{ToolName: "jetkvm_power_action", DeviceID: "stable-1"})
		if decision.Allowed || decision.Reason != DeniedDeploymentCeiling {
			t.Fatalf("confirmation changed permissions: %+v", decision)
		}
	}
}
