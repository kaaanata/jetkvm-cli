package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileReceiptStoreStrictRoundTrip(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "jetkvm")
	receipt := mustReceipt(t, executable, "1.2.3")
	store := FileReceiptStore{}
	if err := store.Save(receipt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ReceiptPath(executable))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := store.Load(executable)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != receipt {
		t.Fatalf("loaded receipt = %+v, want %+v", loaded, receipt)
	}
}

func TestReceiptRejectsUnknownFieldAndExecutableMismatch(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "jetkvm")
	data := `{"schema_version":1,"install_id":"018f3f6a-7c31-7b55-9d22-9b2d756210a4","owner":"standalone","executable":"` + executable + `","version":"1.0.0","repository":"kaaanata/jetkvm-cli","channel":"stable","installed_at":"2026-09-05T00:00:00Z","extra":true}`
	if err := os.WriteFile(ReceiptPath(executable), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (FileReceiptStore{}).Load(executable)
	if kindOf(err) != ErrInvalidReceipt {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrInvalidReceipt)
	}

	receipt := mustReceipt(t, executable, "1.0.0")
	err = receipt.Validate(filepath.Join(dir, "other"))
	if kindOf(err) != ErrReceiptMismatch {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrReceiptMismatch)
	}
}

func TestReceiptContractValidation(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "jetkvm")
	valid := mustReceipt(t, executable, "1.0.0")
	tests := []InstallReceipt{
		func() InstallReceipt { value := valid; value.SchemaVersion = 2; return value }(),
		func() InstallReceipt { value := valid; value.InstallID = "not-a-uuid"; return value }(),
		func() InstallReceipt { value := valid; value.Owner = OwnerHomebrew; return value }(),
		func() InstallReceipt { value := valid; value.Executable = "relative"; return value }(),
		func() InstallReceipt { value := valid; value.Version = "v1.0.0"; return value }(),
		func() InstallReceipt { value := valid; value.Repository = "other/repo"; return value }(),
		func() InstallReceipt { value := valid; value.Channel = "nightly"; return value }(),
		func() InstallReceipt { value := valid; value.InstalledAt = time.Time{}; return value }(),
	}
	for i, receipt := range tests {
		if err := receipt.Validate(receipt.Executable); kindOf(err) != ErrInvalidReceipt {
			t.Fatalf("case %d kind = %q, want %q (error %v)", i, kindOf(err), ErrInvalidReceipt, err)
		}
	}
}

func TestMissingReceiptIsPreservedAsNotExist(t *testing.T) {
	_, err := (FileReceiptStore{}).Load(filepath.Join(t.TempDir(), "jetkvm"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}

func TestUnmanagedReceiptCannotBeCreated(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "jetkvm")
	receipt := mustReceipt(t, executable, "1.0.0")
	receipt.Owner = OwnerUnmanaged
	if err := (FileReceiptStore{}).Save(receipt); kindOf(err) != ErrInvalidReceipt {
		t.Fatalf("kind = %q, want %q", kindOf(err), ErrInvalidReceipt)
	}
	if _, err := os.Stat(ReceiptPath(executable)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmanaged receipt was created: %v", err)
	}
}
