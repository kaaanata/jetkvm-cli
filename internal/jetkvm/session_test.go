package jetkvm

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func TestSessionNegotiatesNewSignalingAndCorrelatesRPC(t *testing.T) {
	t.Parallel()

	device := newFakeSignalingDevice(t, fakeSignalingOptions{unknownMessage: true})
	client := newHTTPTestClient(t, device.server.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	session, err := client.OpenSession(ctx, testSessionConfig())
	if err != nil {
		t.Fatalf("OpenSession() error = %v (answer=%v rpc=%v hid=%v)", err, device.answerSent.Load(), device.rpcOpen.Load(), device.hidOpen.Load())
	}
	if session.Generation() != 1 || session.DeviceVersion() != "0.5.8-test" {
		t.Fatalf("session identity = (%d, %q)", session.Generation(), session.DeviceVersion())
	}

	var pong string
	if err := session.CallRPC(ctx, "ping", map[string]any{}, &pong); err != nil {
		t.Fatalf("CallRPC(ping) error = %v", err)
	}
	if pong != "pong" {
		t.Fatalf("ping result = %q, want pong", pong)
	}
	var accepted bool
	if err := session.CallRPC(ctx, "set-test-value", map[string]any{"value": 1}, &accepted); err != nil {
		t.Fatalf("CallRPC(write) error = %v", err)
	}
	if !accepted {
		t.Fatal("write RPC was not accepted")
	}
	if err := session.SendHID(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatalf("SendHID() error = %v", err)
	}
	if err := session.FlushHID(ctx, session.Generation()); err != nil {
		t.Fatalf("FlushHID() error = %v", err)
	}
	select {
	case payload := <-device.hidPayload:
		if string(payload) != string([]byte{1, 2, 3}) {
			t.Fatalf("HID payload = %v", payload)
		}
	case <-ctx.Done():
		t.Fatal("fake device did not receive HID payload")
	}
	if got := device.connections.Load(); got != 1 {
		t.Fatalf("signaling connections = %d, want one shared session", got)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	waitClosed(t, device.closed)
}

func TestSessionRequiresHIDHandshakeAndFencesFinalSend(t *testing.T) {
	t.Parallel()

	device := newFakeSignalingDevice(t, fakeSignalingOptions{skipHIDHandshake: true})
	client := newHTTPTestClient(t, device.server.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := client.OpenSession(ctx, testSessionConfig()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("OpenSession() error = %v, want HID handshake deadline", err)
	}

	readyDevice := newFakeSignalingDevice(t, fakeSignalingOptions{})
	readyClient := newHTTPTestClient(t, readyDevice.server.URL, nil)
	readyCtx, readyCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer readyCancel()
	session, err := readyClient.OpenSession(readyCtx, testSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.SendHIDForGeneration(readyCtx, session.Generation()+1, []byte{2}); !errors.Is(err, ErrSessionReplaced) {
		t.Fatalf("SendHIDForGeneration() error = %v, want generation fence", err)
	}
}

func TestOpenSessionCancellationClosesSignaling(t *testing.T) {
	t.Parallel()

	accepted := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		close(accepted)
		defer close(closed)
		defer conn.CloseNow()
		_, _, _ = conn.Read(t.Context())
	}))
	t.Cleanup(server.Close)

	client := newHTTPTestClient(t, server.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := client.OpenSession(ctx, SessionConfig{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("OpenSession() error = %v, want deadline exceeded", err)
	}
	waitClosed(t, accepted)
	waitClosed(t, closed)
}

func TestOpenSessionAuthenticatesWithSharedCookieJar(t *testing.T) {
	t.Parallel()

	const password = "session-password"
	device := newFakeSignalingDevice(t, fakeSignalingOptions{password: password})
	client := newHTTPTestClient(t, device.server.URL, CredentialProviderFunc(func(context.Context) ([]byte, error) {
		return []byte(password), nil
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	session, err := client.OpenSession(ctx, testSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := device.loginCalls.Load(); got != 1 {
		t.Fatalf("login calls = %d, want 1", got)
	}
}

func TestSessionCloseFailsPendingRPC(t *testing.T) {
	t.Parallel()

	device := newFakeSignalingDevice(t, fakeSignalingOptions{blockMethod: "wait-forever"})
	client := newHTTPTestClient(t, device.server.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	session, err := client.OpenSession(ctx, testSessionConfig())
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		var value bool
		result <- session.CallRPC(context.Background(), "wait-forever", nil, &value)
	}()
	waitClosed(t, device.blockedRequest)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("pending RPC error = %v, want session closed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending RPC did not finish on session close")
	}
}

func TestCallRPCCancellationRemovesPendingRequest(t *testing.T) {
	t.Parallel()

	device := newFakeSignalingDevice(t, fakeSignalingOptions{blockMethod: "wait-forever"})
	client := newHTTPTestClient(t, device.server.URL, nil)
	openCtx, cancelOpen := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelOpen()
	session, err := client.OpenSession(openCtx, testSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	callCtx, cancelCall := context.WithCancelCause(t.Context())
	result := make(chan error, 1)
	go func() {
		var value bool
		result <- session.CallRPC(callCtx, "wait-forever", nil, &value)
	}()
	waitClosed(t, device.blockedRequest)
	cancelCall(errors.New("caller stopped waiting"))
	if err := <-result; err == nil || err.Error() != "caller stopped waiting" {
		t.Fatalf("CallRPC() error = %v", err)
	}
	session.rpc.mu.Lock()
	pending := len(session.rpc.pending)
	session.rpc.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending RPC count = %d, want 0", pending)
	}
}

func TestOpeningNewSessionFencesPreviousGeneration(t *testing.T) {
	t.Parallel()

	device := newFakeSignalingDevice(t, fakeSignalingOptions{})
	client := newHTTPTestClient(t, device.server.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, err := client.OpenSession(ctx, testSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.OpenSession(ctx, testSessionConfig())
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation() != 1 || second.Generation() != 2 {
		t.Fatalf("generations = (%d, %d)", first.Generation(), second.Generation())
	}
	waitClosed(t, first.Done())
	if !errors.Is(first.Err(), ErrSessionReplaced) {
		t.Fatalf("first session error = %v, want replaced", first.Err())
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

type fakeSignalingOptions struct {
	unknownMessage   bool
	blockMethod      string
	password         string
	skipHIDHandshake bool
}

type fakeSignalingDevice struct {
	server         *httptest.Server
	connections    atomic.Int32
	closed         chan struct{}
	blockedRequest chan struct{}
	closedOnce     sync.Once
	blockedOnce    sync.Once
	opts           fakeSignalingOptions
	answerSent     atomic.Bool
	rpcOpen        atomic.Bool
	hidOpen        atomic.Bool
	hidPayload     chan []byte
	loginCalls     atomic.Int32
}

func newFakeSignalingDevice(t *testing.T, opts fakeSignalingOptions) *fakeSignalingDevice {
	t.Helper()
	device := &fakeSignalingDevice{
		closed:         make(chan struct{}),
		blockedRequest: make(chan struct{}),
		hidPayload:     make(chan []byte, 1),
		opts:           opts,
	}
	device.server = httptest.NewServer(http.HandlerFunc(device.serveHTTP))
	t.Cleanup(device.server.Close)
	return device
}

func (d *fakeSignalingDevice) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/auth/login-local" {
		d.loginCalls.Add(1)
		payload, _ := io.ReadAll(r.Body)
		var request struct {
			Password string `json:"password"`
		}
		if json.Unmarshal(payload, &request) != nil || request.Password != d.opts.password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "authToken", Value: "test-session", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Login successful"}`))
		return
	}
	if r.URL.Path != signalingPath {
		http.NotFound(w, r)
		return
	}
	if d.opts.password != "" {
		cookie, err := r.Cookie("authToken")
		if err != nil || cookie.Value != "test-session" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	d.connections.Add(1)
	defer d.closedOnce.Do(func() { close(d.closed) })
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if d.opts.unknownMessage {
		if err := writeTestSignal(ctx, conn, signalMessage{Type: "future-extension", Data: jsontext.Value(`{"enabled":true}`)}); err != nil {
			return
		}
	}
	if err := writeTestSignal(ctx, conn, signalMessage{Type: "device-metadata", Data: mustRawJSON(struct {
		DeviceVersion string `json:"deviceVersion"`
	}{DeviceVersion: "0.5.8-test"})}); err != nil {
		return
	}

	peer, err := testWebRTCAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return
	}
	defer peer.Close()
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() == "hidrpc" {
			channel.OnOpen(func() { d.hidOpen.Store(true) })
			channel.OnMessage(func(message webrtc.DataChannelMessage) {
				if len(message.Data) == 2 && message.Data[0] == 0x01 && !d.opts.skipHIDHandshake {
					_ = channel.Send(append([]byte(nil), message.Data...))
					return
				}
				select {
				case d.hidPayload <- append([]byte(nil), message.Data...):
				default:
				}
			})
			return
		}
		if channel.Label() != "rpc" {
			return
		}
		channel.OnOpen(func() { d.rpcOpen.Store(true) })
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			var request rpcRequest
			if json.Unmarshal(message.Data, &request) != nil {
				return
			}
			if request.Method == d.opts.blockMethod {
				d.blockedOnce.Do(func() { close(d.blockedRequest) })
				return
			}
			var result any = true
			if request.Method == "ping" {
				result = "pong"
			}
			payload, err := json.Marshal(struct {
				JSONRPC string `json:"jsonrpc"`
				Result  any    `json:"result"`
				ID      string `json:"id"`
			}{JSONRPC: "2.0", Result: result, ID: request.ID})
			if err == nil {
				_ = channel.SendText(string(payload))
			}
		})
	})

	for {
		messageType, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			continue
		}
		var message signalMessage
		if json.Unmarshal(payload, &message) != nil {
			continue
		}
		switch message.Type {
		case "offer":
			var offerData struct {
				SD string `json:"sd"`
			}
			if json.Unmarshal(message.Data, &offerData) != nil {
				return
			}
			description, err := base64.StdEncoding.DecodeString(offerData.SD)
			if err != nil {
				return
			}
			var offer webrtc.SessionDescription
			if json.Unmarshal(description, &offer) != nil || peer.SetRemoteDescription(offer) != nil {
				return
			}
			answer, err := peer.CreateAnswer(nil)
			if err != nil {
				return
			}
			gatheringComplete := webrtc.GatheringCompletePromise(peer)
			if peer.SetLocalDescription(answer) != nil {
				return
			}
			select {
			case <-gatheringComplete:
			case <-ctx.Done():
				return
			}
			encoded, err := json.Marshal(peer.LocalDescription())
			if err != nil {
				return
			}
			err = writeTestSignal(ctx, conn, signalMessage{
				Type: "answer",
				Data: mustRawJSON(base64.StdEncoding.EncodeToString(encoded)),
			})
			if err != nil {
				return
			}
			d.answerSent.Store(true)
		case "new-ice-candidate":
			var candidate webrtc.ICECandidateInit
			if json.Unmarshal(message.Data, &candidate) == nil {
				_ = peer.AddICECandidate(candidate)
			}
		}
	}
}

func writeTestSignal(ctx context.Context, conn *websocket.Conn, message signalMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func mustRawJSON(value any) jsontext.Value {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func waitClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close")
	}
}

func testSessionConfig() SessionConfig {
	return SessionConfig{newPeer: testWebRTCAPI().NewPeerConnection}
}

func testWebRTCAPI() *webrtc.API {
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetIncludeLoopbackCandidate(true)
	settingEngine.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	return webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))
}
