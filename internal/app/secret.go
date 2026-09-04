package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const runtimeSecretName = "runtime.secret"

func loadOrCreateRuntimeSecret(ctx context.Context, directory string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return zero, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return zero, fmt.Errorf("protect state directory: %w", err)
	}

	initializationLock := flock.New(filepath.Join(directory, ".runtime-secret.lock"), flock.SetPermissions(0o600))
	locked, err := initializationLock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return zero, fmt.Errorf("lock runtime secret initialization: %w", err)
	}
	if !locked {
		if cause := context.Cause(ctx); cause != nil {
			return zero, cause
		}
		return zero, errors.New("runtime secret initialization lock was not acquired")
	}
	defer func() { _ = initializationLock.Unlock() }()

	path := filepath.Join(directory, runtimeSecretName)
	secret, err := readRuntimeSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}

	if _, err := rand.Read(secret[:]); err != nil {
		return zero, fmt.Errorf("generate runtime secret: %w", err)
	}
	if err := writeRuntimeSecret(directory, path, secret); err != nil {
		return zero, err
	}
	return secret, nil
}

func readRuntimeSecret(path string) ([sha256.Size]byte, error) {
	var secret [sha256.Size]byte
	info, err := os.Lstat(path)
	if err != nil {
		return secret, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return secret, errors.New("runtime secret must be a regular file with mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return secret, fmt.Errorf("open runtime secret: %w", err)
	}
	defer file.Close()
	if _, err := io.ReadFull(file, secret[:]); err != nil {
		return secret, fmt.Errorf("read runtime secret: %w", err)
	}
	var extra [1]byte
	if count, err := file.Read(extra[:]); err != io.EOF || count != 0 {
		return [sha256.Size]byte{}, errors.New("runtime secret must contain exactly 32 bytes")
	}
	return secret, nil
}

func writeRuntimeSecret(directory, path string, secret [sha256.Size]byte) (err error) {
	temporary, err := os.CreateTemp(directory, ".runtime-secret-*")
	if err != nil {
		return fmt.Errorf("create runtime secret temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect runtime secret temporary file: %w", err)
	}
	if _, err := temporary.Write(secret[:]); err != nil {
		return fmt.Errorf("write runtime secret: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync runtime secret: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close runtime secret: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish runtime secret: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open state directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}
