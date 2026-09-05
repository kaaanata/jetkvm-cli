package video

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func motionRequests(t *testing.T, name string) []DecodeRequest {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".h264")
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte{0, 0, 0, 1, 9}
	var requests []DecodeRequest
	for len(data) > 0 {
		next := bytes.Index(data[5:], marker)
		if next < 0 {
			next = len(data)
		} else {
			next += 5
		}
		au := bytes.Clone(data[:next])
		data = data[next:]
		requests = append(requests, DecodeRequest{AccessUnit: AccessUnit{Generation: 1, RTPTime: uint32(len(requests) + 1), ReceivedAt: time.Now().Add(time.Duration(len(requests)) * time.Millisecond), AnnexB: au, Keyframe: len(requests) == 0, Decodable: true}})
	}
	return requests
}

func TestContinuousDecoderGoldenAndOwnedPlanes(t *testing.T) {
	for _, name := range []string{"motion-p", "motion-b"} {
		t.Run(name, func(t *testing.T) {
			d, _ := EmbeddedDecoder().New()
			t.Cleanup(func() { _ = d.Close() })
			requests := motionRequests(t, name)
			want, err := os.ReadFile("testdata/" + name + ".yuv")
			if err != nil {
				t.Fatal(err)
			}
			var images []*streamImage
			var out []byte
			seen := map[uint32]bool{}
			take := func(f DecodedFrame) {
				if f.Pending {
					return
				}
				if f.Source == nil || seen[f.Source.RTPTime] {
					t.Fatalf("invalid/repeated source %+v", f.Source)
				}
				seen[f.Source.RTPTime] = true
				token := int(f.Source.RTPTime) - 1
				if token < 0 || token >= len(requests) || f.Source.ReceivedAt != requests[token].AccessUnit.ReceivedAt {
					t.Fatal("output source was restamped")
				}
				if len(images) == 0 && f.Source.RTPTime != 1 {
					t.Fatal("first output is not the first source")
				}
				im := f.Image.(*streamImage)
				images = append(images, im)
				for _, p := range im.planes {
					out = append(out, p...)
				}
			}
			for _, r := range requests {
				f, err := d.Decode(t.Context(), r)
				if err != nil {
					t.Fatal(err)
				}
				take(f)
			}
			for range 20 {
				f, err := d.Decode(t.Context(), DecodeRequest{EndOfStream: true})
				if err != nil {
					t.Fatal(err)
				}
				if f.Pending {
					break
				}
				take(f)
			}
			if len(images) != 12 || !bytes.Equal(out, want) {
				for i := range images {
					f := out[i*23040 : (i+1)*23040]
					match := -1
					for j := range 12 {
						if bytes.Equal(f, want[j*23040:(j+1)*23040]) {
							match = j
						}
					}
					t.Logf("output %d matches reference %d", i, match)
				}
				t.Fatalf("frames=%d bytes=%d want=%d exact=%v", len(images), len(out), len(want), bytes.Equal(out, want))
			}
			// All previous images remain unchanged after later decode and module teardown.
			_ = d.Reset()
			var retained []byte
			for _, im := range images {
				for _, p := range im.planes {
					retained = append(retained, p...)
				}
			}
			if !bytes.Equal(out, retained) {
				t.Fatal("published planes alias mutable decoder memory")
			}
			if _, err := d.Decode(t.Context(), requests[1]); !errors.Is(err, ErrDecodeFailed) {
				t.Fatalf("P frame accepted after reset: %v", err)
			}
		})
	}
}

func deltaPacket(seq uint16, stamp uint32) RTPPacket {
	return packet(1, seq, stamp, true, time.Now(), []byte{0x41, 0x80})
}
func TestContinuousQueuePreservesDependenciesAndRecoversOverload(t *testing.T) {
	d := &controlledLiveDecoder{started: make(chan DecodeRequest, 64), release: make(chan struct{})}
	p, _ := NewPipeline("device", 1, Limits{}, d, nil)
	t.Cleanup(func() { _ = p.Close() })
	_ = p.StartLive(t.Context())
	_ = p.PushLive(t.Context(), liveTestPacket(1, 1, 1))
	_ = liveStarted(t, d)
	for i := uint16(2); i <= 4; i++ {
		if err := p.PushLive(t.Context(), deltaPacket(i, uint32(i))); err != nil {
			t.Fatal(err)
		}
	}
	for i := uint32(2); i <= 4; i++ {
		d.release <- struct{}{}
		if got := liveStarted(t, d).AccessUnit.RTPTime; got != i {
			t.Fatalf("dropped reference %d got %d", i, got)
		}
	}
	// Keep fourth frame decoding while filling the bounded dependency queue.
	for i := uint16(5); i <= 37; i++ {
		_ = p.PushLive(t.Context(), deltaPacket(i, uint32(i)))
	}
	p.mu.Lock()
	synced, n := p.synced, len(p.pending)
	p.mu.Unlock()
	if synced || n != 0 {
		t.Fatal("overload retained broken chain")
	}
	if err := p.PushLive(t.Context(), deltaPacket(38, 38)); !errors.Is(err, ErrVideoUnavailable) {
		t.Fatal(err)
	}
	_ = p.PushLive(t.Context(), liveTestPacket(1, 39, 39))
	d.release <- struct{}{}
	if got := liveStarted(t, d).AccessUnit.RTPTime; got != 39 {
		t.Fatalf("recovery got %d", got)
	}
	d.release <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	o, err := p.AwaitObservation(ctx, ObserveRequest{Generation: 1})
	if err != nil || o.Frame.RTPTime != 39 {
		t.Fatalf("recovery observation %+v %v", o, err)
	}
}

func TestDepacketizerInterFrameLossDuplicateAndWrap(t *testing.T) {
	d, _ := NewDepacketizer(1, Limits{})
	first := liveTestPacket(1, 65534, 0xfffffff0)
	if _, err := d.Push(first); err != nil {
		t.Fatal(err)
	}
	// Entire frame 65535 is absent; wrap alone must not hide the gap.
	u, err := d.Push(deltaPacket(0, 1))
	if err != nil || u == nil || !u.Discontinuity {
		t.Fatalf("lost whole frame %+v %v", u, err)
	}
	if u, err := d.Push(first); err != nil || u != nil {
		t.Fatal("late duplicate changed state")
	}
	u, err = d.Push(deltaPacket(1, 2))
	if err != nil || u == nil || u.Discontinuity {
		t.Fatalf("valid wrap %+v %v", u, err)
	}
	// Late packets from a completed AU cannot replace a current partial AU.
	p := packet(1, 2, 3, false, time.Now(), []byte{0x7c, 0x81, 0x80})
	_, _ = d.Push(p)
	_, _ = d.Push(deltaPacket(1, 2))
	u, err = d.Push(packet(1, 3, 3, true, time.Now(), []byte{0x7c, 0x41, 0x80}))
	if err != nil || u == nil || u.Discontinuity {
		t.Fatalf("partial AU destroyed %+v %v", u, err)
	}
}

func TestContinuousGeometryChangeDropsOldReferenceState(t *testing.T) {
	d, _ := EmbeddedDecoder().New()
	t.Cleanup(func() { _ = d.Close() })
	requests := motionRequests(t, "motion-b")
	for _, r := range requests[:4] {
		if _, err := d.Decode(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	// A new smaller IDR changes geometry while old pictures remain buffered.
	small := fixtureRequest(t, "red-high")
	small.AccessUnit.Generation = 2
	small.AccessUnit.RTPTime = 100
	f, err := d.Decode(t.Context(), small)
	if err != nil || f.Pending {
		t.Fatalf("geometry reset %v", err)
	}
	if f.Source.Generation != 2 || f.Source.RTPTime != 100 || f.Image.Bounds().Dx() != 32 {
		t.Fatalf("old frame escaped reset %+v", f.Source)
	}
	if _, err := d.Decode(t.Context(), requests[1]); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("old dependent picture after finite reset %v", err)
	}
}

func TestDepacketizerLargeSequenceGapRecoversAtNewerIDR(t *testing.T) {
	d, _ := NewDepacketizer(1, Limits{})
	_, _ = d.Push(liveTestPacket(1, 1, 1))
	// More than half a sequence space can be lost; newer RTP time fences late data.
	u, err := d.Push(liveTestPacket(1, 40000, 2))
	if err != nil || u == nil || !u.Discontinuity {
		t.Fatalf("large loss did not recover %+v %v", u, err)
	}
}
