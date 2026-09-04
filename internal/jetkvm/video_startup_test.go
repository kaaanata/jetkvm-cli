package jetkvm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// The sender emits no RTP until it receives a PLI. Waiting for OnTrack before
// PLI would deadlock forever: Pion requires first RTP to fire OnTrack.
func TestNegotiatedVideoPLIUnblocksFirstRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	receiver, err := testWebRTCAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := testWebRTCAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	_, err = receiver.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	local, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "test")
	if err != nil {
		t.Fatal(err)
	}
	rtpSender, err := sender.AddTrack(local)
	if err != nil {
		t.Fatal(err)
	}
	gotTrack := make(chan struct{})
	var once sync.Once
	receiver.OnTrack(func(*webrtc.TrackRemote, *webrtc.RTPReceiver) { once.Do(func() { close(gotTrack) }) })
	connected := make(chan struct{})
	var connectedOnce sync.Once
	receiver.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})
	offer, err := receiver.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(receiver)
	if err := receiver.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := sender.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather = webrtc.GatheringCompletePromise(sender)
	if err := sender.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-gotTrack:
		t.Fatal("OnTrack fired without RTP")
	default:
	}
	done := make(chan error, 1)
	var worker sync.WaitGroup
	worker.Go(func() {
		for {
			packets, _, err := rtpSender.ReadRTCP()
			if err != nil {
				done <- err
				return
			}
			for _, packet := range packets {
				if pli, ok := packet.(*rtcp.PictureLossIndication); ok {
					if pli.MediaSSRC != uint32(rtpSender.GetParameters().Encodings[0].SSRC) {
						done <- errors.New("PLI targeted wrong SDP identity")
						return
					}
					done <- local.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1, Marker: true}, Payload: []byte{0x65, 0x88, 0x84}})
					return
				}
			}
		}
	})
	defer func() {
		_ = sender.Close()
		worker.Wait()
	}()
	session := &Session{peer: receiver, generation: 1, done: make(chan struct{})}
	if err := session.RequestNegotiatedVideoKeyframe(ctx, 2); !errors.Is(err, ErrSessionReplaced) {
		t.Fatal(err)
	}
	if err := session.RequestNegotiatedVideoKeyframe(ctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-gotTrack:
	case <-ctx.Done():
		t.Fatal("PLI did not unlock first RTP")
	}
}

func TestNegotiatedVideoPLIRejectsUnavailableAndCanceledSession(t *testing.T) {
	peer, err := testWebRTCAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	session := &Session{peer: peer, generation: 1, done: make(chan struct{})}
	if err := session.RequestNegotiatedVideoKeyframe(t.Context(), 1); !errors.Is(err, ErrWebRTCFailed) {
		t.Fatalf("missing video accepted: %v", err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("caller canceled video")
	cancel(cause)
	if err := session.RequestNegotiatedVideoKeyframe(ctx, 1); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	session.cause = ErrSessionClosed
	close(session.done)
	if err := session.RequestNegotiatedVideoKeyframe(t.Context(), 1); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
}
