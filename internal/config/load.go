package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
)

var ErrMissing = errors.New("JetKVM has not been set up")

func Load(path string) (Config, error) {
	cfg, _, err := LoadSnapshot(path)
	return cfg, err
}

// LoadSnapshot binds a validated configuration and its revision to one file
// read. Callers must not hash a later read as the revision of an older runtime.
func LoadSnapshot(path string) (Config, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Lstat(path); statErr == nil {
				return Config{}, "", errors.New("configuration target is unavailable")
			}
			return Config{}, "", fmt.Errorf("%w: run jetkvm setup", ErrMissing)
		}
		return Config{}, "", fmt.Errorf("open config: %w", err)
	}
	cfg, err := Decode(bytes.NewReader(data))
	if err != nil {
		return Config{}, "", err
	}
	return cfg, Revision(data), nil
}

func Revision(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

func Decode(reader io.Reader) (Config, error) {
	config := Default()
	if err := json.UnmarshalRead(reader, &config, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}
