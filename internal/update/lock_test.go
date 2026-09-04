package update

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockerSerializesUpdates(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "jetkvm")
	first := NewFileLocker(executable)
	second := NewFileLocker(executable)
	unlock, err := first.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.Lock(ctx); kindOf(err) != ErrUpdateInProgress {
		t.Fatalf("kind = %q, want %q (error %v)", kindOf(err), ErrUpdateInProgress, err)
	}
}
