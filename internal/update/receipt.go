package update

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/Masterminds/semver/v3"
)

const (
	receiptFilename         = ".jetkvm-install.json"
	previousReceiptFilename = ".jetkvm-install.previous.json"
)

type InstallReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	InstallID     string    `json:"install_id"`
	Owner         Owner     `json:"owner"`
	Executable    string    `json:"executable"`
	Version       string    `json:"version"`
	Repository    string    `json:"repository"`
	Channel       Channel   `json:"channel"`
	InstalledAt   time.Time `json:"installed_at"`
}

func ReceiptPath(executable string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(executable)), receiptFilename)
}

type ReceiptStore interface {
	Load(executable string) (InstallReceipt, error)
	Save(InstallReceipt) error
	LoadPrevious(executable string) (InstallReceipt, error)
	SavePrevious(InstallReceipt) error
	RemovePrevious(executable string) error
}

type FileReceiptStore struct{}

func (FileReceiptStore) Load(executable string) (InstallReceipt, error) {
	return loadReceipt(ReceiptPath(executable), executable)
}

func (FileReceiptStore) Save(receipt InstallReceipt) error {
	if receipt.Owner == OwnerUnmanaged {
		return newError(ErrInvalidReceipt, "unmanaged installations do not create managed receipts")
	}
	return saveReceipt(ReceiptPath(receipt.Executable), receipt)
}

func (FileReceiptStore) LoadPrevious(executable string) (InstallReceipt, error) {
	return loadReceipt(filepath.Join(filepath.Dir(filepath.Clean(executable)), previousReceiptFilename), executable)
}

func (FileReceiptStore) SavePrevious(receipt InstallReceipt) error {
	if receipt.Owner != OwnerStandalone {
		return newError(ErrInvalidReceipt, "only standalone installations have rollback receipts")
	}
	return saveReceipt(filepath.Join(filepath.Dir(filepath.Clean(receipt.Executable)), previousReceiptFilename), receipt)
}

func (FileReceiptStore) RemovePrevious(executable string) error {
	err := os.Remove(filepath.Join(filepath.Dir(filepath.Clean(executable)), previousReceiptFilename))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func loadReceipt(path, executable string) (InstallReceipt, error) {
	file, err := os.Open(path)
	if err != nil {
		return InstallReceipt{}, err
	}
	defer file.Close()

	var receipt InstallReceipt
	if err := json.UnmarshalRead(file, &receipt, json.RejectUnknownMembers(true)); err != nil {
		return InstallReceipt{}, newError(ErrInvalidReceipt, "decode install receipt: %v", err)
	}
	if err := receipt.Validate(executable); err != nil {
		return InstallReceipt{}, err
	}
	return receipt, nil
}

func saveReceipt(path string, receipt InstallReceipt) error {
	if err := receipt.Validate(receipt.Executable); err != nil {
		return err
	}
	data, err := json.Marshal(receipt, json.Deterministic(true))
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".jetkvm-install-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (r InstallReceipt) Validate(currentExecutable string) error {
	if r.SchemaVersion != 1 {
		return newError(ErrInvalidReceipt, "unsupported install receipt schema_version %d", r.SchemaVersion)
	}
	if _, err := uuid.Parse(r.InstallID); err != nil {
		return newError(ErrInvalidReceipt, "install_id must be a UUID: %v", err)
	}
	if r.Owner != OwnerStandalone && r.Owner != OwnerUnmanaged {
		return newError(ErrInvalidReceipt, "portable receipt owner must be standalone or unmanaged")
	}
	if !filepath.IsAbs(r.Executable) || filepath.Clean(r.Executable) != r.Executable {
		return newError(ErrInvalidReceipt, "executable must be an absolute cleaned path")
	}
	current, err := filepath.Abs(currentExecutable)
	if err != nil {
		return newError(ErrReceiptMismatch, "resolve current executable: %v", err)
	}
	if filepath.Clean(current) != r.Executable {
		return newError(ErrReceiptMismatch, "install receipt executable does not match current executable")
	}
	if strings.HasPrefix(r.Version, "v") {
		return newError(ErrInvalidReceipt, "version must not have a leading v")
	}
	if _, err := semver.StrictNewVersion(r.Version); err != nil {
		return newError(ErrInvalidReceipt, "version must be strict semantic version: %v", err)
	}
	if r.Repository != Repository {
		return newError(ErrInvalidReceipt, "repository must be %q", Repository)
	}
	if r.Channel != ChannelStable && r.Channel != ChannelPrerelease {
		return newError(ErrInvalidReceipt, "channel must be stable or prerelease")
	}
	if r.InstalledAt.IsZero() {
		return newError(ErrInvalidReceipt, "installed_at must be RFC3339")
	}
	return nil
}

func (r InstallReceipt) Installation() Installation {
	return Installation{
		InstallID: r.InstallID, Owner: r.Owner, Executable: r.Executable,
		Version: r.Version, Repository: r.Repository, Channel: r.Channel, InstalledAt: r.InstalledAt,
	}
}

func NewStandaloneReceipt(executable, version string, channel Channel, installedAt time.Time) (InstallReceipt, error) {
	abs, err := filepath.Abs(executable)
	if err != nil {
		return InstallReceipt{}, fmt.Errorf("resolve executable: %w", err)
	}
	receipt := InstallReceipt{
		SchemaVersion: 1,
		InstallID:     uuid.NewV7().String(),
		Owner:         OwnerStandalone,
		Executable:    filepath.Clean(abs),
		Version:       strings.TrimPrefix(version, "v"),
		Repository:    Repository,
		Channel:       channel,
		InstalledAt:   installedAt,
	}
	return receipt, receipt.Validate(receipt.Executable)
}
