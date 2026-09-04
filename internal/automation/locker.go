package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
)

const lockRetryInterval = 50 * time.Millisecond

var ErrDeviceLocked = errors.New("device control is locked by another process")

// FileLocker implements the process-wide device ownership boundary with an
// operating-system file lock. Lock file names contain only a digest of the
// stable device identity.
type FileLocker struct {
	directory string
}

func NewFileLocker(directory string) (*FileLocker, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, errors.New("device lock directory must be an absolute path")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create device lock directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect device lock directory: %w", err)
	}
	return &FileLocker{directory: directory}, nil
}

func (l *FileLocker) Acquire(ctx context.Context, deviceID domain.DeviceID) (control.Lock, error) {
	if deviceID == "" {
		return nil, errors.New("device ID is required for locking")
	}
	digest := sha256.Sum256([]byte(deviceID))
	path := filepath.Join(l.directory, hex.EncodeToString(digest[:])+".lock")
	fileLock := flock.New(path, flock.SetPermissions(0o600))
	locked, err := fileLock.TryLockContext(ctx, lockRetryInterval)
	if err != nil {
		return nil, fmt.Errorf("acquire device file lock: %w", err)
	}
	if !locked {
		return nil, ErrDeviceLocked
	}
	return &heldFileLock{lock: fileLock}, nil
}

type heldFileLock struct {
	lock *flock.Flock
}

func (l *heldFileLock) Release() error {
	if err := l.lock.Unlock(); err != nil {
		return fmt.Errorf("release device file lock: %w", err)
	}
	return nil
}

var _ control.Locker = (*FileLocker)(nil)
