package jetkvm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/credentials"
	"github.com/kaaanata/jetkvm-cli/internal/jetkvm"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

// Explicitly enabled HIL capture; it opens a takeover session but sends no HID.
func TestHILCaptureKeyframe(t *testing.T) {
	path := os.Getenv("JETKVM_HIL_CONFIG")
	output := os.Getenv("JETKVM_HIL_FRAME")
	if path == "" || output == "" {
		t.Skip("HIL capture not requested")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) != 1 {
		t.Fatal("capture requires one explicitly selected device")
	}
	for _, device := range cfg.Devices {
		provider, err := credentials.New(device.Credentials)
		if err != nil {
			t.Fatal(err)
		}
		client, err := jetkvm.NewClient(jetkvm.Config{Origin: device.Origin, AllowPlainHTTP: device.AllowPlainHTTP, Credentials: provider})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		session, err := client.OpenSession(ctx, jetkvm.SessionConfig{})
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		var track jetkvm.VideoTrack
		select {
		case track = <-session.VideoTracks():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		t.Logf("video codec %s", track.Track.Codec().MimeType)
		depacketizer, err := video.NewDepacketizer(session.Generation(), video.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if err := session.RequestVideoKeyframe(ctx, session.Generation(), uint32(track.Track.SSRC())); err != nil {
			t.Fatal(err)
		}
		for ctx.Err() == nil {
			track.Track.SetReadDeadline(time.Now().Add(5 * time.Second))
			packet, _, err := track.Track.ReadRTP()
			if err != nil {
				t.Fatal(err)
			}
			unit, err := depacketizer.Push(video.RTPPacket{Generation: session.Generation(), SequenceNumber: packet.SequenceNumber, Timestamp: packet.Timestamp, Marker: packet.Marker, ReceivedAt: time.Now(), Payload: packet.Payload})
			if err != nil {
				continue
			}
			if unit != nil && unit.Keyframe && unit.Decodable {
				if err := os.WriteFile(output, unit.AnnexB, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Logf("captured IDR bytes=%d", len(unit.AnnexB))
				return
			}
		}
		t.Fatal(ctx.Err())
	}
}
