package mcpserver

import (
	"context"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/confirmation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeDeviceService struct {
	devices              []domain.Device
	status               domain.DeviceStatus
	capabilities         domain.CapabilitySnapshot
	statusDetail         domain.StatusDetail
	capabilitiesExtended bool
}

type fakeAutomationService struct {
	openCalls  int
	powerCalls int
}

func (*fakeAutomationService) PrepareOpenControl(request automation.OpenControlRequest) (automation.ConfirmationPlan, error) {
	return automation.ConfirmationPlan{Required: true, Binding: testConfirmationBinding(request.DeviceID, 0, domain.EffectObserve, "control.takeover")}, nil
}

func (f *fakeAutomationService) OpenControl(_ context.Context, request automation.OpenControlRequest) (control.Handle, error) {
	f.openCalls++
	return control.Handle{
		ID: "ctl-1", DeviceID: request.DeviceID, Generation: 7, Ownership: control.OwnershipOwned,
		Capabilities: request.Capabilities, State: control.HandleReady,
	}, nil
}

func (*fakeAutomationService) GetControl(context.Context, automation.ControlRequest) (control.Snapshot, error) {
	return control.Snapshot{}, nil
}

func (*fakeAutomationService) CloseControl(context.Context, automation.ControlRequest) (control.Handle, error) {
	return control.Handle{}, nil
}

func (*fakeAutomationService) RunActions(context.Context, automation.RunActionsRequest) (automation.RunActionsResult, error) {
	return automation.RunActionsResult{}, nil
}

func (*fakeAutomationService) PrepareRunActions(request automation.RunActionsRequest) (automation.ConfirmationPlan, error) {
	return automation.ConfirmationPlan{Required: true, Binding: testConfirmationBinding(request.DeviceID, request.Ref.ExpectedGeneration, domain.EffectInput, "input.commit")}, nil
}

func (*fakeAutomationService) GetPowerState(context.Context, automation.ControlRequest) (automation.PowerState, error) {
	return automation.PowerState{}, nil
}

func (f *fakeAutomationService) PowerAction(_ context.Context, request automation.PowerActionRequest) (operation.Receipt, error) {
	f.powerCalls++
	now := time.Date(2026, time.September, 5, 1, 2, 3, 0, time.UTC)
	return operation.Receipt{
		Request: operation.Request{
			ID: request.OperationID, DeviceID: request.DeviceID, ControlGeneration: request.Ref.ExpectedGeneration,
			Effect: operation.EffectPower, Action: "power." + string(request.Action), PolicyRevision: "policy-test",
		},
		Stage: operation.StageCompleted, Delivery: operation.DeliveryTransportAccepted,
		Verification:  operation.Verification{Status: operation.VerificationNotRequested},
		TerminalClaim: operation.TerminalClaimNotProven, CreatedAt: now, UpdatedAt: now, TerminalAt: now,
	}, nil
}

func (*fakeAutomationService) PreparePowerAction(request automation.PowerActionRequest) (automation.ConfirmationPlan, error) {
	required := request.Action == automation.PowerReset || request.Action == automation.PowerHold
	return automation.ConfirmationPlan{Required: required, Binding: testConfirmationBinding(request.DeviceID, request.Ref.ExpectedGeneration, domain.EffectPower, "power."+string(request.Action))}, nil
}

func testConfirmationBinding(deviceID domain.DeviceID, generation uint64, effect domain.EffectClass, action string) confirmation.Binding {
	return confirmation.Binding{DeviceID: deviceID, Generation: generation, Effect: effect, Action: action, ArgumentsDigest: confirmation.DigestArguments([]byte(action)), PolicyRevision: "policy-test"}
}

func (f *fakeDeviceService) ListDevices(context.Context) ([]domain.Device, error) {
	return slices.Clone(f.devices), nil
}

func (f *fakeDeviceService) GetStatus(_ context.Context, _ domain.DeviceID, detail domain.StatusDetail) (domain.DeviceStatus, error) {
	f.statusDetail = detail
	return f.status, nil
}

func (f *fakeDeviceService) GetCapabilities(_ context.Context, _ domain.DeviceID, extended bool) (domain.CapabilitySnapshot, error) {
	f.capabilitiesExtended = extended
	return f.capabilities, nil
}

func TestNewRejectsNilDeviceService(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("New(nil) succeeded, want error")
	}
}

func TestMCPConfirmationMintsProofForSharedExecutionAuthority(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	authority, err := confirmation.NewAuthority(confirmation.Config{Key: key, Nonces: confirmation.NewMemoryNonceStore()})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewConfirmationIssuer(key, time.Minute, authority)
	if err != nil {
		t.Fatal(err)
	}
	binding := testConfirmationBinding("device-1", 7, domain.EffectInput, "input.commit")
	request := ConfirmationRequest{
		Principal: "test-client/1", DeviceID: binding.DeviceID, Generation: binding.Generation,
		OperationKind: binding.Action, ArgumentsDigest: hex.EncodeToString(binding.ArgumentsDigest[:]), PolicyRevision: binding.PolicyRevision, Binding: binding,
	}
	state, err := issuer.Issue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := issuer.Confirm(t.Context(), state, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.VerifyAndConsume(confirmed, binding); err != nil {
		t.Fatalf("shared execution authority rejected MCP proof: %v", err)
	}
	if _, err := issuer.Confirm(t.Context(), state, request); !errors.Is(err, ErrConfirmationReplayed) {
		t.Fatalf("challenge replay error = %v", err)
	}
}

func TestAllowedToolsIsAStaticCeiling(t *testing.T) {
	adapter, err := New(&fakeDeviceService{}, Options{
		AllowedTools: map[string]bool{toolListDevices: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := connectInMemory(t, adapter.newProtocolServer())
	var names []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{toolListDevices}) {
		t.Fatalf("tools = %v, want only %s", names, toolListDevices)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      toolGetStatus,
		Arguments: map[string]any{"device_id": "device-1"},
	})
	if err == nil && !result.IsError {
		t.Fatal("direct call bypassed the static tool ceiling")
	}
}

func TestControlToolCeilingRejectsDirectCall(t *testing.T) {
	automationService := &fakeAutomationService{}
	issuer, err := NewConfirmationIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(&fakeDeviceService{}, Options{
		AllowedTools: map[string]bool{toolOpenControl: true}, Automation: automationService,
		ConfirmationIssuer: issuer, PolicyRevision: "policy-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := connectInMemory(t, adapter.newProtocolServer())
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: toolRunActions, Arguments: map[string]any{}})
	if err == nil && !result.IsError {
		t.Fatal("direct call bypassed AllowedTools ceiling")
	}
	if automationService.openCalls != 0 {
		t.Fatal("automation service was called by denied tool")
	}
}

func TestTakeoverMRTRRejectsTamperAndReplay(t *testing.T) {
	automationService := &fakeAutomationService{}
	issuer, err := NewConfirmationIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(&fakeDeviceService{}, Options{
		AllowedTools: map[string]bool{toolOpenControl: true}, Automation: automationService,
		ConfirmationIssuer: issuer, PolicyRevision: "policy-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := connectInMemoryWithOptions(t, adapter.newProtocolServer(), &mcp.ClientOptions{
		Capabilities:   &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{}},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	params := &mcp.CallToolParams{
		Name:      toolOpenControl,
		Arguments: map[string]any{"device_id": "device-1", "requested_capabilities": []string{"video"}},
	}
	first, err := session.CallTool(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if !first.NeedsInput() || first.RequestState == "" {
		t.Fatalf("first call = %#v, want input_required with sealed state", first)
	}
	params.InputResponses = mcp.InputResponseMap{
		confirmationInputID: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true, "device_id": "device-1"}},
	}
	params.RequestState = first.RequestState + "tampered"
	tampered, err := session.CallTool(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if !tampered.IsError || automationService.openCalls != 0 {
		t.Fatal("tampered confirmation reached automation")
	}

	params.InputResponses = mcp.InputResponseMap{confirmationInputID: &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}}
	params.RequestState = first.RequestState
	accepted, err := session.CallTool(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.IsError || automationService.openCalls != 1 {
		t.Fatalf("confirmed call error=%v openCalls=%d", accepted.IsError, automationService.openCalls)
	}
	replayed, err := session.CallTool(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.IsError || automationService.openCalls != 1 {
		t.Fatal("replayed confirmation reached automation")
	}
}

func TestPowerActionReturnsStructuredOperationReceipt(t *testing.T) {
	automationService := &fakeAutomationService{}
	adapter, err := New(&fakeDeviceService{}, Options{
		AllowedTools: map[string]bool{toolPowerAction: true}, Automation: automationService,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := connectInMemory(t, adapter.newProtocolServer())
	operationID := uuid.NewV7()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: toolPowerAction,
		Arguments: map[string]any{
			"device_id": "device-1", "expected_device_id": "device-1", "control_handle": "ctl-1",
			"expected_generation": 7, "operation_id": operationID.String(), "action": "press",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || automationService.powerCalls != 1 {
		t.Fatalf("power action error=%v calls=%d content=%#v", result.IsError, automationService.powerCalls, result.Content)
	}
	operationResult := structuredMap(t, result.StructuredContent)["operation"].(map[string]any)
	if operationResult["operation_id"] != operationID.String() || operationResult["delivery"] != "transport_accepted" {
		t.Fatalf("operation receipt = %#v", operationResult)
	}
}

func TestObserveToolsExposeStrictSchemasAndStructuredContent(t *testing.T) {
	observedAt := time.Date(2026, time.September, 5, 2, 3, 4, 0, time.UTC)
	service := &fakeDeviceService{
		devices: []domain.Device{{
			ID:              "device-1",
			Alias:           "lab",
			Origin:          "https://user@example.test:8443/private?token=secret#fragment",
			Exposed:         true,
			Permissions:     []string{"observe"},
			TakeoverAllowed: false,
		}},
		status: domain.DeviceStatus{
			DeviceID:  "device-1",
			Alias:     "lab",
			Observed:  observedAt,
			Reachable: true,
			Fields: map[string]domain.FieldObservation{
				"app_version": {Value: "0.5.8", Source: "http", ObservedAt: observedAt},
			},
		},
		capabilities: domain.CapabilitySnapshot{
			DeviceID: "device-1",
			Alias:    "lab",
			Observed: observedAt,
			Items: []domain.CapabilityState{{
				Name:              "observe",
				Compiled:          true,
				Configured:        true,
				FirmwareSupported: true,
				CurrentlyReady:    true,
			}},
		},
	}
	adapter, err := New(service, Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	clientSession := connectInMemory(t, adapter.newProtocolServer())

	tools := map[string]*mcp.Tool{}
	for tool, err := range clientSession.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = tool
	}
	if len(tools) != 3 {
		t.Fatalf("got %d tools, want 3", len(tools))
	}
	for _, name := range []string{toolListDevices, toolGetStatus, toolGetCapabilities} {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("tool %q is not registered", name)
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("tool %q is missing input or output schema", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Fatalf("tool %q has incorrect read-only annotations: %#v", name, tool.Annotations)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("tool %q must explicitly declare destructiveHint=false", name)
		}
		assertClosedObjectSchema(t, name+" input", tool.InputSchema)
		assertClosedObjectSchema(t, name+" output", tool.OutputSchema)
	}

	statusSchema := schemaMap(t, tools[toolGetStatus].InputSchema)
	detail := statusSchema["properties"].(map[string]any)["detail"].(map[string]any)
	if got := detail["enum"].([]any); len(got) != 1 || got[0] != "basic" {
		t.Fatalf("status detail enum = %#v, want [basic]", got)
	}

	listed, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: toolListDevices})
	if err != nil {
		t.Fatal(err)
	}
	if listed.IsError {
		t.Fatalf("list devices returned tool error: %#v", listed.Content)
	}
	structured := structuredMap(t, listed.StructuredContent)
	devices := structured["devices"].([]any)
	device := devices[0].(map[string]any)
	if got := device["origin"]; got != "https://example.test:8443" {
		t.Fatalf("sanitized origin = %q", got)
	}

	status, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      toolGetStatus,
		Arguments: map[string]any{"device_id": "device-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.IsError || service.statusDetail != domain.StatusBasic {
		t.Fatalf("status result error=%v detail=%q", status.IsError, service.statusDetail)
	}
	if structuredMap(t, status.StructuredContent)["status"] == nil {
		t.Fatal("status structuredContent is missing status")
	}

	capabilities, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      toolGetCapabilities,
		Arguments: map[string]any{"device_id": "device-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.IsError || service.capabilitiesExtended {
		t.Fatalf("capabilities result error=%v extended=%v", capabilities.IsError, service.capabilitiesExtended)
	}
}

func TestGetStatusRejectsNonBasicDetailBeforeCallingService(t *testing.T) {
	service := &fakeDeviceService{}
	adapter, err := New(service, Options{})
	if err != nil {
		t.Fatal(err)
	}
	clientSession := connectInMemory(t, adapter.newProtocolServer())

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: toolGetStatus,
		Arguments: map[string]any{
			"device_id": "device-1",
			"detail":    "standard",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("non-basic status detail succeeded, want tool validation error")
	}
	if service.statusDetail != "" {
		t.Fatalf("service was called with detail %q", service.statusDetail)
	}
}

func TestToolSchemasRejectUnknownPropertiesAndEmptyDeviceIdentity(t *testing.T) {
	service := &fakeDeviceService{}
	adapter, err := New(service, Options{})
	if err != nil {
		t.Fatal(err)
	}
	clientSession := connectInMemory(t, adapter.newProtocolServer())

	tests := []mcp.CallToolParams{
		{Name: toolListDevices, Arguments: map[string]any{"unexpected": true}},
		{Name: toolGetStatus, Arguments: map[string]any{"device_id": ""}},
		{Name: toolGetCapabilities, Arguments: map[string]any{"device_id": ""}},
	}
	for _, test := range tests {
		result, err := clientSession.CallTool(t.Context(), &test)
		if err != nil {
			t.Fatalf("CallTool(%q): %v", test.Name, err)
		}
		if !result.IsError {
			t.Errorf("CallTool(%q) accepted invalid input %#v", test.Name, test.Arguments)
		}
	}
}

func TestResourcesUseStableIdentityAndHTTPOnlyReads(t *testing.T) {
	service := &fakeDeviceService{
		devices:      []domain.Device{{ID: "device-1", Alias: "lab", Origin: "http://jetkvm.test", Exposed: true}},
		status:       domain.DeviceStatus{DeviceID: "device-1", Alias: "lab", Observed: time.Now(), Reachable: true},
		capabilities: domain.CapabilitySnapshot{DeviceID: "device-1", Alias: "lab", Observed: time.Now(), Items: []domain.CapabilityState{}},
	}
	adapter, err := New(service, Options{})
	if err != nil {
		t.Fatal(err)
	}
	clientSession := connectInMemory(t, adapter.newProtocolServer())

	tests := []struct {
		uri        string
		wantTTL    int
		wantDetail domain.StatusDetail
	}{
		{devicesResourceURI, devicesResourceTTLMs, ""},
		{"jetkvm://devices/device-1", devicesResourceTTLMs, ""},
		{"jetkvm://devices/device-1/status", statusResourceTTLMs, domain.StatusBasic},
		{"jetkvm://devices/device-1/capabilities", capabilitiesResourceTTLMs, domain.StatusBasic},
	}
	for _, test := range tests {
		result, err := clientSession.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: test.uri})
		if err != nil {
			t.Fatalf("ReadResource(%q): %v", test.uri, err)
		}
		if result.TTLMs != test.wantTTL || result.CacheScope != "private" {
			t.Fatalf("ReadResource(%q) cache = (%d, %q)", test.uri, result.TTLMs, result.CacheScope)
		}
		if len(result.Contents) != 1 || result.Contents[0].MIMEType != resourceMIMEType {
			t.Fatalf("ReadResource(%q) contents = %#v", test.uri, result.Contents)
		}
		var decoded any
		if err := json.Unmarshal([]byte(result.Contents[0].Text), &decoded); err != nil {
			t.Fatalf("ReadResource(%q) invalid JSON: %v", test.uri, err)
		}
	}
	if service.statusDetail != domain.StatusBasic {
		t.Fatalf("status resource used detail %q, want basic", service.statusDetail)
	}
	if service.capabilitiesExtended {
		t.Fatal("capability resource requested extended/device-session probe")
	}
}

func TestStatelessHTTPServerRequiresLoopbackAndBearer(t *testing.T) {
	adapter, err := New(&fakeDeviceService{}, Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"0.0.0.0:8080", "192.0.2.1:8080", "localhost:8080", ":8080"} {
		if _, err := adapter.NewStatelessHTTPServer(HTTPConfig{ListenAddress: address, BearerToken: "secret"}); err == nil {
			t.Errorf("NewStatelessHTTPServer(%q) succeeded, want loopback validation error", address)
		}
	}

	httpServer, err := adapter.NewStatelessHTTPServer(HTTPConfig{
		ListenAddress: "127.0.0.1:0",
		BearerToken:   "secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	unauthenticated := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(requestBody))
	unauthenticatedRecorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(unauthenticatedRecorder, unauthenticated)
	if unauthenticatedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthenticatedRecorder.Code)
	}

	authenticated := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(requestBody))
	authenticated.Header.Set("Authorization", "Bearer secret")
	authenticated.Header.Set("Accept", "application/json, text/event-stream")
	authenticated.Header.Set("Content-Type", "application/json")
	authenticated.Header.Set("MCP-Protocol-Version", "2026-07-28")
	authenticated.Header.Set("Mcp-Method", "server/discover")
	authenticatedRecorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(authenticatedRecorder, authenticated)
	response := authenticatedRecorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("authenticated status = %d, body = %s", response.StatusCode, body)
	}
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		t.Fatalf("stateless response returned session ID %q", sessionID)
	}

	if _, err := adapter.NewStatelessHTTPServer(HTTPConfig{ListenAddress: "[::1]:0", BearerToken: "secret"}); err != nil {
		t.Fatalf("IPv6 loopback listen address rejected: %v", err)
	}

	rebinding := httptest.NewRequest(http.MethodPost, "http://attacker.example/mcp", strings.NewReader(requestBody))
	rebinding.RemoteAddr = "127.0.0.1:12345"
	rebinding = rebinding.WithContext(context.WithValue(
		rebinding.Context(),
		http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	))
	rebinding.Header.Set("Authorization", "Bearer secret")
	rebinding.Header.Set("Accept", "application/json, text/event-stream")
	rebinding.Header.Set("Content-Type", "application/json")
	rebinding.Header.Set("MCP-Protocol-Version", "2026-07-28")
	rebinding.Header.Set("Mcp-Method", "server/discover")
	rebindingRecorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(rebindingRecorder, rebinding)
	if rebindingRecorder.Code != http.StatusForbidden {
		t.Fatalf("DNS-rebinding request status = %d, want 403", rebindingRecorder.Code)
	}
}

func TestBearerMiddlewareRejectsMalformedCredentials(t *testing.T) {
	middleware, err := BearerMiddleware("secret")
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))

	for _, authorization := range []string{"", "Basic secret", "Bearer wrong", "Bearer  secret"} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", nil)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q status = %d, want 401", authorization, recorder.Code)
		}
	}
}

func connectInMemory(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	return connectInMemoryWithOptions(t, server, nil)
}

func connectInMemoryWithOptions(t *testing.T, server *mcp.Server, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, options)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func assertClosedObjectSchema(t *testing.T, name string, schema any) {
	t.Helper()
	decoded := schemaMap(t, schema)
	if got := decoded["type"]; got != "object" {
		t.Fatalf("%s type = %q, want object", name, got)
	}
	if got, ok := decoded["additionalProperties"]; !ok || got != false {
		t.Fatalf("%s additionalProperties = %#v, want false", name, got)
	}
}

func schemaMap(t *testing.T, schema any) map[string]any {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func structuredMap(t *testing.T, structured any) map[string]any {
	t.Helper()
	data, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestApprovalOnlyDeclineCancelAndInvalidRemainUnexecuted(t *testing.T) {
	for _, action := range []string{"decline", "cancel", "unknown"} {
		t.Run(action, func(t *testing.T) {
			backend := &fakeAutomationService{}
			issuer, err := NewConfirmationIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			adapter, _ := New(&fakeDeviceService{}, Options{Automation: backend, ConfirmationIssuer: issuer, PolicyRevision: "policy-test"})
			session := connectInMemoryWithOptions(t, adapter.newProtocolServer(), &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{}}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
			params := &mcp.CallToolParams{Name: toolOpenControl, Arguments: map[string]any{"device_id": "device-1", "requested_capabilities": []string{"video"}}}
			first, err := session.CallTool(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			params.RequestState = first.RequestState
			params.InputResponses = mcp.InputResponseMap{confirmationInputID: &mcp.ElicitResult{Action: action}}
			result, err := session.CallTool(t.Context(), params)
			if backend.openCalls != 0 {
				t.Fatal("rejected approval opened control")
			}
			if err == nil && !result.IsError {
				t.Fatal("invalid approval accepted")
			}
		})
	}
}
