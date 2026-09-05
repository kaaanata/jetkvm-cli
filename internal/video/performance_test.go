package video

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A private or synthetic length-prefixed Annex-B AU stream may be replayed
// without connecting to a device. This records decode/copy cost, not network age.
func TestContinuousDecoderReplayPerformance(t *testing.T) {
	path := os.Getenv("JETKVM_VIDEO_REPLAY")
	if path == "" {
		t.Skip("replay benchmark not requested")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	for len(data) >= 4 {
		n := int(binary.LittleEndian.Uint32(data))
		data = data[4:]
		if n <= 0 || n > len(data) {
			t.Fatal("bad replay record")
		}
		units = append(units, bytes.Clone(data[:n]))
		data = data[n:]
	}
	if len(data) != 0 || len(units) == 0 {
		t.Fatal("bad replay")
	}
	if value := os.Getenv("JETKVM_VIDEO_REPLAY_ROUNDS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			t.Fatal("invalid rounds")
		}
		original := units
		units = nil
		for range n {
			units = append(units, original...)
		}
	}
	d, _ := EmbeddedDecoder().New()
	t.Cleanup(func() { _ = d.Close() })
	var expected []string
	if path := os.Getenv("JETKVM_VIDEO_REFERENCE_MD5"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Split(line, ",")
			expected = append(expected, strings.TrimSpace(fields[len(fields)-1]))
		}
	}
	verify := func(f DecodedFrame, index int) {
		if expected == nil {
			return
		}
		im := f.Image.(*streamImage)
		h := md5.New()
		for _, plane := range im.planes {
			_, _ = h.Write(plane)
		}
		if index >= len(expected) || fmt.Sprintf("%x", h.Sum(nil)) != expected[index] {
			t.Fatalf("reference mismatch at frame %d", index)
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	count := 0
	var first time.Duration
	for i, au := range units {
		key := false
		for _, n := range bytes.Split(au, []byte{0, 0, 1}) {
			if len(n) > 0 && n[0]&31 == 5 {
				key = true
			}
		}
		f, err := d.Decode(t.Context(), DecodeRequest{AccessUnit: AccessUnit{Generation: 1, RTPTime: uint32(i + 1), ReceivedAt: time.Now(), AnnexB: au, Keyframe: key, Decodable: true}})
		if err != nil {
			t.Fatal(err)
		}
		if !f.Pending {
			verify(f, count)
			count++
			if first == 0 {
				first = time.Since(start)
			}
		}
	}
	for range 32 {
		f, err := d.Decode(t.Context(), DecodeRequest{EndOfStream: true})
		if err != nil {
			t.Fatal(err)
		}
		if f.Pending {
			break
		}
		verify(f, count)
		count++
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if count != len(units) {
		t.Fatalf("frames %d want %d", count, len(units))
	}
	t.Logf("frames=%d elapsed=%s throughput=%.1f fps first_output=%s Go_allocated=%d MiB Go_heap=%d MiB", count, elapsed, float64(count)/elapsed.Seconds(), first, (after.TotalAlloc-before.TotalAlloc)>>20, after.HeapAlloc>>20)
}
