package jetkvm

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

var (
	ErrSessionClosed   = errors.New("JetKVM session is closed")
	ErrSessionReplaced = errors.New("JetKVM session was replaced")
	ErrWebRTCFailed    = errors.New("JetKVM WebRTC negotiation failed")
	ErrHIDProtocol     = errors.New("JetKVM HID-RPC handshake failed")
)

const (
	outboundSignalBuffer = 32
	hidRPCVersion        = byte(1)
	hidHandshakeAttempts = 10
	hidHandshakeInterval = time.Second
)

// SessionConfig configures the local WebRTC session. ICE servers are normally
// empty for direct LAN access.
type SessionConfig struct {
	ConnectTimeout time.Duration
	ICEServers     []webrtc.ICEServer
	newPeer        func(webrtc.Configuration) (*webrtc.PeerConnection, error)
}

// VideoTrack identifies a remote media track and the session generation that
// owns it. Consumers must discard tracks from obsolete generations.
type VideoTrack struct {
	Generation uint64
	Track      *webrtc.TrackRemote
	Receiver   *webrtc.RTPReceiver
}

// Session owns one JetKVM WebSocket signaling connection, PeerConnection, and
// the rpc/hidrpc DataChannels negotiated by that connection.
type Session struct {
	client     *Client
	generation uint64
	deviceVer  string
	signal     *signalSocket
	peer       *webrtc.PeerConnection
	rpcChannel *webrtc.DataChannel
	hidChannel *webrtc.DataChannel
	rpc        *rpcMux

	ctx       context.Context
	cancel    context.CancelCauseFunc
	done      chan struct{}
	closeOnce sync.Once
	workers   sync.WaitGroup

	answerReady chan struct{}
	rpcReady    chan struct{}
	hidOpened   chan struct{}
	hidReady    chan struct{}
	hidLow      chan struct{}
	outbound    chan signalMessage
	video       chan VideoTrack
	candidateMu sync.Mutex
	offerSent   bool
	preOffer    []signalMessage

	errMu    sync.Mutex
	closeErr error
	cause    error
}

// OpenSession establishes the current new-signaling JetKVM session. A newly
// ready session fences and closes the previous session owned by this Client.
func (c *Client) OpenSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	c.openMu.Lock()
	defer c.openMu.Unlock()

	c.sessionMu.Lock()
	c.generation++
	generation := c.generation
	c.sessionMu.Unlock()

	session, err := c.openSession(ctx, cfg, generation)
	if err != nil {
		return nil, err
	}

	c.sessionMu.Lock()
	previous := c.active
	c.active = session
	c.sessionMu.Unlock()
	if previous != nil {
		previous.shutdown(ErrSessionReplaced)
	}
	return session, nil
}

func (c *Client) openSession(ctx context.Context, cfg SessionConfig, generation uint64) (*Session, error) {
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	if timeout < 0 {
		return nil, errors.New("connect timeout must be positive")
	}
	ctx, cancelConnect := context.WithTimeout(ctx, timeout)
	defer cancelConnect()
	signal, err := c.dialSignaling(ctx)
	if err != nil {
		return nil, err
	}

	deviceVersion, err := awaitDeviceMetadata(ctx, signal)
	if err != nil {
		_ = signal.conn.CloseNow()
		return nil, fmt.Errorf("wait for device metadata: %w", err)
	}

	newPeer := cfg.newPeer
	if newPeer == nil {
		newPeer = webrtc.NewPeerConnection
	}
	peer, err := newPeer(webrtc.Configuration{ICEServers: cfg.ICEServers})
	if err != nil {
		_ = signal.conn.CloseNow()
		return nil, ErrWebRTCFailed
	}
	sessionCtx, cancel := context.WithCancelCause(context.Background())
	session := &Session{
		client:      c,
		generation:  generation,
		deviceVer:   deviceVersion,
		signal:      signal,
		peer:        peer,
		rpc:         newRPCMux(generation),
		ctx:         sessionCtx,
		cancel:      cancel,
		done:        make(chan struct{}),
		answerReady: make(chan struct{}),
		rpcReady:    make(chan struct{}),
		hidOpened:   make(chan struct{}),
		hidReady:    make(chan struct{}),
		hidLow:      make(chan struct{}, 1),
		outbound:    make(chan signalMessage, outboundSignalBuffer),
		video:       make(chan VideoTrack, 1),
	}
	if err := session.configurePeer(); err != nil {
		session.shutdown(err)
		session.wait()
		return nil, err
	}

	session.workers.Go(session.writeSignals)
	session.workers.Go(session.readSignals)
	session.workers.Go(session.negotiateHID)
	if err := session.sendOffer(ctx); err != nil {
		session.shutdown(err)
		session.wait()
		return nil, err
	}
	if err := session.waitReady(ctx); err != nil {
		session.shutdown(err)
		session.wait()
		return nil, err
	}
	return session, nil
}

func awaitDeviceMetadata(ctx context.Context, signal *signalSocket) (string, error) {
	for {
		message, err := signal.read(ctx)
		if err != nil {
			return "", err
		}
		if len(message.Error) != 0 {
			return "", ErrSignalingFailed
		}
		if message.Type != "device-metadata" {
			continue
		}
		var metadata struct {
			DeviceVersion string `json:"deviceVersion"`
		}
		if err := json.Unmarshal(message.Data, &metadata); err != nil {
			return "", ErrInvalidSignalingMessage
		}
		if metadata.DeviceVersion == "" {
			return "", ErrLegacySignaling
		}
		return metadata.DeviceVersion, nil
	}
}

func (s *Session) configurePeer() error {
	transceiver, err := s.peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	})
	if err != nil {
		return ErrWebRTCFailed
	}
	// The embedded snapshot decoder accepts H.264 only. Never negotiate a
	// preferred firmware H.265 stream that the observation pipeline cannot read.
	var codecs []webrtc.RTPCodecParameters
	for _, codec := range transceiver.Receiver().GetParameters().Codecs {
		if codec.MimeType == webrtc.MimeTypeH264 {
			codecs = append(codecs, codec)
		}
	}
	if len(codecs) == 0 {
		return ErrWebRTCFailed
	}
	if err := transceiver.SetCodecPreferences(codecs); err != nil {
		return ErrWebRTCFailed
	}

	rpcChannel, err := s.peer.CreateDataChannel("rpc", nil)
	if err != nil {
		return ErrWebRTCFailed
	}
	hidChannel, err := s.peer.CreateDataChannel("hidrpc", nil)
	if err != nil {
		return ErrWebRTCFailed
	}
	s.rpcChannel = rpcChannel
	s.hidChannel = hidChannel

	var rpcOpen sync.Once
	rpcChannel.OnOpen(func() { rpcOpen.Do(func() { close(s.rpcReady) }) })
	rpcChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		s.rpc.deliver(s.generation, message.Data)
	})
	hidChannel.SetBufferedAmountLowThreshold(0)
	hidChannel.OnBufferedAmountLow(func() {
		select {
		case s.hidLow <- struct{}{}:
		default:
		}
	})
	var hidOpen sync.Once
	hidChannel.OnOpen(func() { hidOpen.Do(func() { close(s.hidOpened) }) })
	var hidHandshake sync.Once
	hidChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		if len(message.Data) == 0 || message.Data[0] != 0x01 {
			return
		}
		if len(message.Data) != 2 || message.Data[1] == 0 || message.Data[1] > hidRPCVersion {
			s.shutdown(ErrHIDProtocol)
			return
		}
		hidHandshake.Do(func() { close(s.hidReady) })
	})

	s.peer.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		select {
		case s.video <- VideoTrack{Generation: s.generation, Track: track, Receiver: receiver}:
		default:
		}
	})
	s.peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		data, err := rawJSON(candidate.ToJSON())
		if err != nil {
			s.shutdown(ErrInvalidSignalingMessage)
			return
		}
		message := signalMessage{Type: "new-ice-candidate", Data: data}
		s.candidateMu.Lock()
		if !s.offerSent {
			s.preOffer = append(s.preOffer, message)
			s.candidateMu.Unlock()
			return
		}
		s.candidateMu.Unlock()
		s.enqueueSignal(message)
	})
	s.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed {
			s.shutdown(ErrSessionClosed)
		}
	})
	return nil
}

func (s *Session) negotiateHID() {
	select {
	case <-s.hidOpened:
	case <-s.ctx.Done():
		return
	}

	ticker := time.NewTicker(hidHandshakeInterval)
	defer ticker.Stop()
	for attempt := range hidHandshakeAttempts {
		if err := s.hidChannel.Send([]byte{0x01, hidRPCVersion}); err != nil {
			s.shutdown(ErrHIDChannelUnavailable)
			return
		}
		select {
		case <-s.hidReady:
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if attempt == hidHandshakeAttempts-1 {
				s.shutdown(ErrHIDProtocol)
				return
			}
		}
	}
}

func (s *Session) sendOffer(ctx context.Context) error {
	offer, err := s.peer.CreateOffer(nil)
	if err != nil {
		return ErrWebRTCFailed
	}
	if err := s.peer.SetLocalDescription(offer); err != nil {
		return ErrWebRTCFailed
	}
	local := s.peer.LocalDescription()
	if local == nil {
		return ErrWebRTCFailed
	}
	description, err := json.Marshal(local)
	if err != nil {
		return ErrWebRTCFailed
	}
	data, err := rawJSON(struct {
		SD string `json:"sd"`
	}{SD: base64.StdEncoding.EncodeToString(description)})
	if err != nil {
		return ErrInvalidSignalingMessage
	}
	if err := s.signal.write(ctx, signalMessage{Type: "offer", Data: data}); err != nil {
		return err
	}
	s.candidateMu.Lock()
	s.offerSent = true
	pending := s.preOffer
	s.preOffer = nil
	s.candidateMu.Unlock()
	for _, message := range pending {
		s.enqueueSignal(message)
	}
	return nil
}

func (s *Session) enqueueSignal(message signalMessage) {
	select {
	case s.outbound <- message:
	case <-s.ctx.Done():
	}
}

func (s *Session) readSignals() {
	var pendingCandidates []webrtc.ICECandidateInit
	answerApplied := false
	for {
		message, err := s.signal.read(s.ctx)
		if err != nil {
			s.shutdown(err)
			return
		}
		if len(message.Error) != 0 {
			s.shutdown(ErrSignalingFailed)
			return
		}
		switch message.Type {
		case "answer":
			if err := s.applyAnswer(message.Data); err != nil {
				s.shutdown(err)
				return
			}
			answerApplied = true
			for _, candidate := range pendingCandidates {
				if err := s.peer.AddICECandidate(candidate); err != nil {
					s.shutdown(ErrWebRTCFailed)
					return
				}
			}
			pendingCandidates = nil
		case "new-ice-candidate":
			var candidate webrtc.ICECandidateInit
			if err := json.Unmarshal(message.Data, &candidate); err != nil || candidate.Candidate == "" {
				continue
			}
			if !answerApplied {
				pendingCandidates = append(pendingCandidates, candidate)
				continue
			}
			if err := s.peer.AddICECandidate(candidate); err != nil {
				s.shutdown(ErrWebRTCFailed)
				return
			}
		default:
			// Forward compatibility: unknown signaling messages do not alter the
			// authoritative offer/answer/ICE state machine.
		}
	}
}

func (s *Session) applyAnswer(data jsontext.Value) error {
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil || encoded == "" {
		return ErrInvalidSignalingMessage
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ErrInvalidSignalingMessage
	}
	var answer webrtc.SessionDescription
	if err := json.Unmarshal(payload, &answer); err != nil || answer.Type != webrtc.SDPTypeAnswer {
		return ErrInvalidSignalingMessage
	}
	if err := s.peer.SetRemoteDescription(answer); err != nil {
		return ErrWebRTCFailed
	}
	select {
	case <-s.answerReady:
	default:
		close(s.answerReady)
	}
	return nil
}

func (s *Session) writeSignals() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case message := <-s.outbound:
			if err := s.signal.write(s.ctx, message); err != nil {
				s.shutdown(err)
				return
			}
		}
	}
}

func (s *Session) waitReady(ctx context.Context) error {
	for index, ready := range []<-chan struct{}{s.answerReady, s.rpcReady, s.hidReady} {
		select {
		case <-ready:
		case <-s.done:
			return s.Err()
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w", []string{"SDP answer", "RPC channel", "HID handshake"}[index], context.Cause(ctx))
		}
	}
	return nil
}

// CallRPC sends one JSON-RPC request over this session's rpc DataChannel and
// correlates its response. It never retries a request.
func (s *Session) CallRPC(ctx context.Context, method string, params any, result any) error {
	if method == "" {
		return ErrRPCProtocol
	}
	select {
	case <-s.done:
		return s.Err()
	default:
	}
	if s.rpcChannel.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrRPCChannelUnavailable
	}
	id, response, err := s.rpc.register()
	if err != nil {
		return err
	}
	if params == nil {
		params = map[string]any{}
	}
	request := rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: id}
	payload, err := json.Marshal(request)
	if err != nil {
		s.rpc.remove(id)
		return ErrRPCProtocol
	}
	if err := s.rpcChannel.SendText(string(payload)); err != nil {
		s.rpc.remove(id)
		return ErrRPCChannelUnavailable
	}
	raw, err := waitRPC(ctx, response)
	if err != nil {
		s.rpc.remove(id)
		return err
	}
	if result == nil {
		return nil
	}
	if len(raw) == 0 || json.Unmarshal(raw, result) != nil {
		return ErrRPCProtocol
	}
	return nil
}

// SendHID sends one already-validated binary HID protocol message. Business
// actions, input leases, and neutralization are intentionally owned above this
// protocol adapter.
func (s *Session) SendHID(ctx context.Context, payload []byte) error {
	return s.SendHIDForGeneration(ctx, s.generation, payload)
}

// SendHIDForGeneration performs the final generation fence immediately before
// enqueueing one HID-RPC message on the negotiated channel.
func (s *Session) SendHIDForGeneration(ctx context.Context, generation uint64, payload []byte) error {
	if generation != s.generation {
		return ErrSessionReplaced
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-s.done:
		return s.Err()
	default:
	}
	if s.hidChannel.ReadyState() != webrtc.DataChannelStateOpen {
		return ErrHIDChannelUnavailable
	}
	if err := s.hidChannel.Send(payload); err != nil {
		return ErrHIDChannelUnavailable
	}
	return nil
}

// FlushHID waits until Pion reports that all application bytes queued on the
// reliable HID DataChannel have drained. It does not claim that the attached
// host has processed those reports.
func (s *Session) FlushHID(ctx context.Context, generation uint64) error {
	if generation != s.generation {
		return ErrSessionReplaced
	}
	for s.hidChannel.BufferedAmount() != 0 {
		select {
		case <-s.hidLow:
		case <-s.done:
			return s.Err()
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		if generation != s.generation {
			return ErrSessionReplaced
		}
	}
	return nil
}

// Generation returns the immutable generation assigned to this session.
func (s *Session) Generation() uint64 { return s.generation }

// DeviceVersion returns the version announced by new WebSocket signaling.
func (s *Session) DeviceVersion() string { return s.deviceVer }

// VideoTracks yields remote video tracks owned by this generation.
func (s *Session) VideoTracks() <-chan VideoTrack { return s.video }

// Done closes when this session has been fenced or closed.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err reports the terminal session cause.
func (s *Session) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.cause == nil {
		return ErrSessionClosed
	}
	return s.cause
}

// Close explicitly fences the generation, fails pending RPCs, and closes all
// signaling and WebRTC resources owned by the session.
func (s *Session) Close() error {
	return s.CloseContext(context.Background())
}

// CloseContext fences the session immediately, then waits up to the caller's
// deadline for signaling workers to finish and resources to report closure.
func (s *Session) CloseContext(ctx context.Context) error {
	s.shutdown(ErrSessionClosed)
	finished := make(chan struct{})
	go func() {
		s.wait()
		close(finished)
	}()
	select {
	case <-finished:
		return s.resourceError()
	case <-ctx.Done():
		return fmt.Errorf("close JetKVM session: %w", context.Cause(ctx))
	}
}

func (s *Session) shutdown(cause error) {
	s.closeOnce.Do(func() {
		s.errMu.Lock()
		s.cause = cause
		s.errMu.Unlock()
		s.rpc.close(cause)
		s.cancel(cause)

		peerErr := s.peer.Close()
		signalErr := s.signal.conn.CloseNow()
		s.errMu.Lock()
		s.closeErr = errors.Join(peerErr, signalErr)
		s.errMu.Unlock()
		close(s.done)

		s.client.sessionMu.Lock()
		if s.client.active == s {
			s.client.active = nil
		}
		s.client.sessionMu.Unlock()
	})
}

func (s *Session) wait() { s.workers.Wait() }

func (s *Session) resourceError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.closeErr
}
