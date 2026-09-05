package jetkvm_test

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/credentials"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

type startupDecoder struct {
	video.Decoder
	t          *testing.T
	start      time.Time
	calls      atomic.Int64
	continuous bool
	record     *os.File
	mu         sync.Mutex
	durations  []time.Duration
	ages       []time.Duration
	outputs    int
}

func (d *startupDecoder) Decode(ctx context.Context, r video.DecodeRequest) (video.DecodedFrame, error) {
	d.calls.Add(1)
	if !d.continuous {
		d.t.Logf("decode_start elapsed=%s source_age=%s bytes=%d", time.Since(d.start), time.Since(r.AccessUnit.ReceivedAt), len(r.AccessUnit.AnnexB))
	}
	if d.record != nil {
		var size [4]byte
		binary.LittleEndian.PutUint32(size[:], uint32(len(r.AccessUnit.AnnexB)))
		if _, err := d.record.Write(size[:]); err != nil {
			return video.DecodedFrame{}, err
		}
		if _, err := d.record.Write(r.AccessUnit.AnnexB); err != nil {
			return video.DecodedFrame{}, err
		}
	}
	now := time.Now()
	frame, err := d.Decoder.Decode(ctx, r)
	if !d.continuous {
		d.t.Logf("decode_end duration=%s success=%t canceled=%t", time.Since(now), err == nil, ctx.Err() != nil)
	}
	d.mu.Lock()
	d.durations = append(d.durations, time.Since(now))
	if frame.Image != nil {
		d.outputs++
		source := r.AccessUnit.ReceivedAt
		if frame.Source != nil {
			source = frame.Source.ReceivedAt
		}
		d.ages = append(d.ages, time.Since(source))
	}
	d.mu.Unlock()
	return frame, err
}

type startupPLI struct {
	session    *jetkvm.Session
	t          *testing.T
	start      time.Time
	ready      chan struct{}
	ssrc       uint32
	count      atomic.Int64
	negotiated bool
}

func (p *startupPLI) RequestPLI(ctx context.Context, _ uint64) error {
	if p.negotiated {
		p.count.Add(1)
		p.t.Logf("negotiated_PLI elapsed=%s", time.Since(p.start))
		return p.session.RequestNegotiatedVideoKeyframe(ctx, p.session.Generation())
	}
	select {
	case <-ctx.Done():
		p.t.Log("PLI blocked waiting for first track")
		return ctx.Err()
	case <-p.ready:
	}
	p.count.Add(1)
	p.t.Logf("PLI elapsed=%s", time.Since(p.start))
	return p.session.RequestVideoKeyframe(ctx, p.session.Generation(), p.ssrc)
}

// Opt-in diagnostic only: opens video and sends RTCP PLI, never HID. Logs
// contain timings/counts only, not addresses, identities, SDP or image content.
func TestHILVideoStartupTimeline(t *testing.T) {
	path := os.Getenv("JETKVM_HIL_CONFIG")
	if path == "" {
		t.Skip("HIL not requested")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal("configuration load failed")
	}
	if len(cfg.Devices) != 1 {
		t.Fatal("one explicit target required")
	}
	for _, device := range cfg.Devices {
		provider, err := credentials.New(device.Credentials)
		if err != nil {
			t.Fatal("credentials unavailable")
		}
		client, err := jetkvm.NewClient(jetkvm.Config{Origin: device.Origin, AllowPlainHTTP: device.AllowPlainHTTP, Credentials: provider})
		if err != nil {
			t.Fatal("client initialization failed")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
		defer cancel()
		start := time.Now()
		session, err := client.OpenSession(ctx, jetkvm.SessionConfig{})
		if err != nil {
			t.Fatal("session open failed")
		}
		defer session.Close()
		t.Logf("session_ready elapsed=%s", time.Since(start))
		pli := &startupPLI{session: session, t: t, start: start, ready: make(chan struct{}), negotiated: os.Getenv("JETKVM_HIL_NEGOTIATED_PLI") == "1"}
		decoder, err := video.EmbeddedDecoder().New()
		if err != nil {
			t.Fatal(err)
		}
		traced := &startupDecoder{Decoder: decoder, t: t, start: start, continuous: os.Getenv("JETKVM_HIL_CONTINUOUS") == "1"}
		if path := os.Getenv("JETKVM_HIL_RECORD"); path != "" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			traced.record = file
		}
		pipeline, err := video.NewPipeline("diagnostic", session.Generation(), video.Limits{}, traced, pli)
		if err != nil {
			t.Fatal(err)
		}
		defer pipeline.Close()
		streamCtx, stop := context.WithCancel(ctx)
		if err := pipeline.StartLive(streamCtx); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var packets, gaps, assemblyErrors, readTimeouts atomic.Int64
		wg.Go(func() {
			var track jetkvm.VideoTrack
			select {
			case <-streamCtx.Done():
				return
			case track = <-session.VideoTracks():
			}
			t.Logf("track_ready elapsed=%s h264=%t", time.Since(start), track.Track.Codec().MimeType == video.CodecH264)
			pli.ssrc = uint32(track.Track.SSRC())
			close(pli.ready)
			var previous uint16
			for streamCtx.Err() == nil {
				_ = track.Track.SetReadDeadline(time.Now().Add(time.Second))
				packet, _, err := track.Track.ReadRTP()
				received := time.Now()
				if err != nil {
					if n, ok := errors.AsType[net.Error](err); ok && n.Timeout() {
						readTimeouts.Add(1)
						continue
					}
					t.Log("reader terminal error")
					return
				}
				n := packets.Add(1)
				if n == 1 {
					t.Logf("first_RTP elapsed=%s", time.Since(start))
				} else if packet.SequenceNumber != previous+1 {
					gaps.Add(1)
				}
				previous = packet.SequenceNumber
				err = pipeline.PushLive(streamCtx, video.RTPPacket{Generation: session.Generation(), SequenceNumber: packet.SequenceNumber, Timestamp: packet.Timestamp, Marker: packet.Marker, ReceivedAt: received, Payload: packet.Payload})
				if err != nil && !errors.Is(err, video.ErrVideoUnavailable) {
					assemblyErrors.Add(1)
				}
			}
		})
		defer func() { stop(); wg.Wait(); _ = pipeline.Close() }()
		observeCtx, done := context.WithTimeout(ctx, 15*time.Second)
		defer done()
		obs, err := pipeline.AwaitObservation(observeCtx, video.ObserveRequest{Generation: session.Generation(), NotBefore: time.Now()})
		t.Logf("observe_end elapsed=%s success=%t stale=%t unavailable=%t packets=%d gaps=%d assembly_errors=%d read_timeouts=%d pli=%d decodes=%d", time.Since(start), err == nil, errors.Is(err, video.ErrFrameStale), errors.Is(err, video.ErrVideoUnavailable), packets.Load(), gaps.Load(), assemblyErrors.Load(), readTimeouts.Load(), pli.count.Load(), traced.calls.Load())
		if err != nil {
			t.Fatal("observation failed; see timing counters")
		}
		t.Logf("image=%dx%d source_age=%s", obs.Frame.Width, obs.Frame.Height, time.Since(obs.CapturedAt))
		if traced.continuous {
			began := time.Now()
			initial := traced.calls.Load()
			select {
			case <-ctx.Done():
				t.Fatal("continuous test canceled")
			case <-time.After(20 * time.Second):
			}
			elapsed := time.Since(began)
			stop()
			wg.Wait()
			_ = pipeline.Close()
			traced.mu.Lock()
			defer traced.mu.Unlock()
			slices.Sort(traced.durations)
			slices.Sort(traced.ages)
			if len(traced.ages) == 0 {
				t.Fatal("no decoded frames")
			}
			t.Logf("continuous interval=%s input_fps=%.1f outputs=%d decode_p95=%s source_age_p95=%s source_age_max=%s packets=%d sequence_gaps=%d assembly_errors=%d", elapsed, float64(traced.calls.Load()-initial)/elapsed.Seconds(), traced.outputs, traced.durations[len(traced.durations)*95/100], traced.ages[len(traced.ages)*95/100], traced.ages[len(traced.ages)-1], packets.Load(), gaps.Load(), assemblyErrors.Load())
		}
	}
}
