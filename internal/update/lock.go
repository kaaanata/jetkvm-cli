package update

import (
	"context"
	"time"

	"github.com/gofrs/flock"
)

type Locker interface {
	Lock(context.Context) (func() error, error)
}

type FileLocker struct {
	path string
}

func NewFileLocker(executable string) *FileLocker {
	return &FileLocker{path: executable + ".update.lock"}
}

func (l *FileLocker) Lock(ctx context.Context) (func() error, error) {
	lock := flock.New(l.path)
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, &Error{Kind: ErrUpdateInProgress, Message: "wait for update lock", Cause: err}
	}
	if !locked {
		return nil, newError(ErrUpdateInProgress, "another update owns the update lock")
	}
	return lock.Unlock, nil
}
