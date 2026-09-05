package onboarding

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBrowserVisualFixture is an opt-in local-only preview, analogous to the
// terminal PTY fixture. It never uses an operator's device or configuration.
func TestBrowserVisualFixture(t *testing.T) {
	if os.Getenv("JETKVM_TEST_BROWSER_PREVIEW") != "1" {
		t.Skip("manual browser preview")
	}
	s, _ := New(Options{Path: filepath.Join(t.TempDir(), "config.json")})
	b, err := NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, err := b.Begin(Draft{Address: "http://jetkvm.local", Name: "Lab JetKVM"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PREVIEW_URL=%s\n", p.URL)
	select {
	case <-t.Context().Done():
	case <-time.After(60 * time.Second):
	}
}

func TestBrowserRejectsForeignOriginAndHost(t *testing.T) {
	s, _ := New(Options{Path: filepath.Join(t.TempDir(), "config.json")})
	b, err := NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, err := b.Begin(Draft{Name: `<script>alert(1)</script>`})
	if err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"origin", "host"} {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", p.URL, strings.NewReader("approve=yes"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if which == "origin" {
			req.Header.Set("Origin", "https://attacker.invalid")
		} else {
			req.Host = "attacker.invalid"
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("%s accepted: %d", which, resp.StatusCode)
		}
	}
	response, err := http.Get(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if strings.Contains(string(body), "<script>alert") || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("unsafe page: %s", body)
	}
	for range 7 {
		if _, err := b.Begin(Draft{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Begin(Draft{}); err == nil {
		t.Fatal("unbounded setup sessions")
	}
}

func TestStatusDoesNotDeadlockWhileCommitAwaitsOwner(t *testing.T) {
	device := fixtureDevice(t, "fixture", "")
	entered, release := make(chan struct{}), make(chan struct{})
	s, _ := New(Options{Path: filepath.Join(t.TempDir(), "config.json"), Change: func(ctx context.Context, _ string, commit func() (Receipt, error)) (Receipt, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return Receipt{}, ctx.Err()
		case <-release:
			return commit()
		}
	}})
	b, err := NewBrowser(s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	p, _ := b.Begin(Draft{})
	done := make(chan error, 1)
	go func() {
		response, err := http.PostForm(p.URL, url.Values{"address": {device.URL}, "trusted_http": {"yes"}, "approve": {"yes"}})
		if err == nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	<-entered
	status := make(chan error, 1)
	go func() { _, err := b.Status(p.ID); status <- err }()
	select {
	case err := <-status:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("status blocked on commit")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := b.Status(p.ID)
	if err != nil || result.Status != "connected" {
		t.Fatalf("status=%+v %v", result, err)
	}
}
