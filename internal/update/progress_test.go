package update

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kaaanata/jetkvm-cli/internal/progress"
)

func TestDownloadReportsBytesWithoutChangingPayload(t *testing.T) {
	for _, known := range []bool{false, true} {
		payload := bytes.Repeat([]byte("x"), 128<<10)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if known {
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			} else {
				w.(http.Flusher).Flush()
			}
			_, _ = w.Write(payload)
		}))
		var events []progress.Event
		ctx := progress.WithObserver(t.Context(), func(e progress.Event) { events = append(events, e) })
		b := &githubBackend{http: server.Client(), allowHTTP: true}
		got, err := b.downloadAsset(ctx, server.URL, 1<<20, "Downloading archive")
		server.Close()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("download changed: %v", err)
		}
		last := events[len(events)-1]
		if last.Completed != int64(len(payload)) {
			t.Fatalf("bytes=%d", last.Completed)
		}
		if known && last.Total != last.Completed || !known && last.Total != 0 {
			t.Fatalf("incorrect total: %+v", last)
		}
	}
}

func TestCanceledDownloadReturnsWithoutInventingCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reports := 0
	ctx = progress.WithObserver(ctx, func(e progress.Event) {
		reports++
		if reports > 1 && e.Total == 0 {
			cancel()
		}
	})
	b := &githubBackend{http: server.Client(), allowHTTP: true}
	if _, err := b.downloadAsset(ctx, server.URL, 1<<20, "Downloading archive"); err == nil {
		t.Fatal("canceled download succeeded")
	}
}
