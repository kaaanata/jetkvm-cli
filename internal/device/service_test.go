package device

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
)

func TestServiceGetStatusPinsIdentityAndAttributesFields(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device/status":
			_, _ = w.Write([]byte(`{"isSetup":true}`))
		case "/device":
			_, _ = w.Write([]byte(`{"authMode":"noPassword","deviceId":"hardware-1","loopbackOnly":false}`))
		case "/cloud/state":
			_, _ = w.Write([]byte(`{"connected":true,"url":"wss://cloud.example","appUrl":"https://cloud.example"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newJetKVMTestClient(t, server.URL)
	service := newTestService(t, client, true, func() time.Time { return observedAt })

	status, err := service.GetStatus(t.Context(), "hardware-1", domain.StatusBasic)
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if status.DeviceID != "hardware-1" || status.Alias != "lab" || !status.Reachable || status.Observed != observedAt {
		t.Fatalf("GetStatus() metadata = %+v", status)
	}

	wantSources := map[string]string{
		"setup":           "/device/status",
		"auth_mode":       "/device",
		"loopback_only":   "/device",
		"cloud_connected": "/cloud/state",
		"cloud_url":       "/cloud/state",
		"cloud_app_url":   "/cloud/state",
	}
	for name, wantSource := range wantSources {
		observation, ok := status.Fields[name]
		if !ok {
			t.Errorf("field %q missing", name)
			continue
		}
		if observation.Source != wantSource {
			t.Errorf("field %q source = %q, want %q", name, observation.Source, wantSource)
		}
		if observation.ObservedAt != observedAt {
			t.Errorf("field %q observed at = %v, want %v", name, observation.ObservedAt, observedAt)
		}
	}
}

func TestServiceGetStatusRejectsIdentityMismatchBeforeCloudRead(t *testing.T) {
	t.Parallel()

	var cloudCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device/status":
			_, _ = w.Write([]byte(`{"isSetup":true}`))
		case "/device":
			_, _ = w.Write([]byte(`{"authMode":"noPassword","deviceId":"different-hardware","loopbackOnly":false}`))
		case "/cloud/state":
			cloudCalls.Add(1)
			_, _ = w.Write([]byte(`{"connected":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newJetKVMTestClient(t, server.URL)
	service := newTestService(t, client, true, time.Now)

	_, err := service.GetStatus(t.Context(), "hardware-1", domain.StatusBasic)
	if !errors.Is(err, domain.ErrDeviceIdentityMismatch) {
		t.Fatalf("GetStatus() error = %v, want identity mismatch", err)
	}
	if got := cloudCalls.Load(); got != 0 {
		t.Fatalf("cloud reads = %d, want 0 after identity mismatch", got)
	}
}

func TestServiceReportsOptionalCloudStateAsUnavailable(t *testing.T) {
	t.Parallel()

	client := &fakeHTTPClient{
		origin:   "https://jetkvm.example",
		setup:    jetkvm.DeviceSetup{IsSetup: true},
		device:   jetkvm.LocalDevice{AuthMode: "noPassword", DeviceID: "hardware-1"},
		cloudErr: errors.New("cloud endpoint unavailable"),
	}
	service := newTestService(t, client, true, time.Now)
	status, err := service.GetStatus(t.Context(), "hardware-1", domain.StatusBasic)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cloud_connected", "cloud_url", "cloud_app_url"} {
		field := status.Fields[name]
		if field.Unavailable == "" || field.Value != nil {
			t.Fatalf("field %q = %+v, want unavailable without value", name, field)
		}
	}
}

func TestServiceOnlyPublishesExposedDevicesAndClonesMutableFields(t *testing.T) {
	t.Parallel()

	client := &fakeHTTPClient{origin: "https://jetkvm.example"}
	service := newTestService(t, client, true, time.Now)
	service.targets["hidden"] = Target{
		Device: domain.Device{ID: "hidden", Alias: "hidden", Origin: client.origin, Exposed: false},
		Client: client,
	}

	devices, err := service.ListDevices(t.Context())
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if len(devices) != 1 || devices[0].ID != "hardware-1" {
		t.Fatalf("ListDevices() = %+v", devices)
	}
	devices[0].Permissions[0] = "power"
	devices[0].Labels["room"] = "changed"
	if service.targets["hardware-1"].Device.Permissions[0] != "observe" {
		t.Fatal("ListDevices() returned shared permissions storage")
	}
	if service.targets["hardware-1"].Device.Labels["room"] != "lab" {
		t.Fatal("ListDevices() returned shared labels storage")
	}
}

func TestServiceCanonicalizesConfiguredRootOrigin(t *testing.T) {
	t.Parallel()

	client := &fakeHTTPClient{origin: "https://jetkvm.example"}
	service, err := NewService(ServiceConfig{Targets: []Target{{
		Device: domain.Device{
			ID:      "hardware-1",
			Alias:   "lab",
			Origin:  "https://jetkvm.example/",
			Exposed: true,
		},
		Client: client,
	}}})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	devices, err := service.ListDevices(t.Context())
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if got := devices[0].Origin; got != "https://jetkvm.example" {
		t.Fatalf("canonical origin = %q", got)
	}
}

func TestServiceRejectsUnexposedAndNonBasicReads(t *testing.T) {
	t.Parallel()

	client := &fakeHTTPClient{origin: "https://jetkvm.example"}
	service := newTestService(t, client, false, time.Now)
	_, err := service.GetStatus(t.Context(), "hardware-1", domain.StatusBasic)
	if !errors.Is(err, domain.ErrDeviceNotExposed) {
		t.Fatalf("unexposed GetStatus() error = %v", err)
	}

	service.targets["hardware-1"] = Target{
		Device: domain.Device{ID: "hardware-1", Alias: "lab", Origin: client.origin, Exposed: true},
		Client: client,
	}
	_, err = service.GetStatus(t.Context(), "hardware-1", domain.StatusStandard)
	if !errors.Is(err, domain.ErrCapabilityUnavailable) {
		t.Fatalf("standard GetStatus() error = %v", err)
	}
}

type fakeHTTPClient struct {
	origin     string
	setup      jetkvm.DeviceSetup
	device     jetkvm.LocalDevice
	cloud      jetkvm.CloudState
	setupErr   error
	deviceErr  error
	cloudErr   error
	cloudCalls int
}

func (f *fakeHTTPClient) Origin() string { return f.origin }

func (f *fakeHTTPClient) GetDeviceStatus(context.Context) (jetkvm.DeviceSetup, error) {
	return f.setup, f.setupErr
}

func (f *fakeHTTPClient) GetDevice(context.Context) (jetkvm.LocalDevice, error) {
	return f.device, f.deviceErr
}

func (f *fakeHTTPClient) GetCloudState(context.Context) (jetkvm.CloudState, error) {
	f.cloudCalls++
	return f.cloud, f.cloudErr
}

func newTestService(t *testing.T, client HTTPClient, exposed bool, now func() time.Time) *Service {
	t.Helper()
	service, err := NewService(ServiceConfig{
		Targets: []Target{{
			Device: domain.Device{
				ID:          "hardware-1",
				Alias:       "lab",
				Origin:      client.Origin(),
				Exposed:     exposed,
				Permissions: []string{"observe"},
				Labels:      map[string]string{"room": "lab"},
			},
			Client: client,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func newJetKVMTestClient(t *testing.T, origin string) *jetkvm.Client {
	t.Helper()
	client, err := jetkvm.NewClient(jetkvm.Config{Origin: origin, AllowPlainHTTP: true})
	if err != nil {
		t.Fatalf("jetkvm.NewClient() error = %v", err)
	}
	return client
}
