package setup

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"uuid"
)

type Store struct {
	directory string
}

func NewStore(directory string) (*Store, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, errors.New("setup store directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create setup store: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect setup store: %w", err)
	}
	return &Store{directory: directory}, nil
}

func (s *Store) Save(receipt Receipt) error {
	if receipt.ID == uuid.Nil() {
		return errors.New("setup receipt ID is required")
	}
	data, err := json.Marshal(receipt, json.Deterministic(true))
	if err != nil {
		return fmt.Errorf("encode setup receipt: %w", err)
	}
	finalPath := filepath.Join(s.directory, receipt.ID.String()+".json")
	temporary, err := os.CreateTemp(s.directory, ".receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("create setup receipt: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect setup receipt: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write setup receipt: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync setup receipt: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close setup receipt: %w", err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return fmt.Errorf("commit setup receipt: %w", err)
	}
	return nil
}

func (s *Store) Load(id uuid.UUID) (Receipt, error) {
	data, err := os.ReadFile(filepath.Join(s.directory, id.String()+".json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Receipt{}, ErrReceiptNotFound
		}
		return Receipt{}, fmt.Errorf("read setup receipt: %w", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt, json.RejectUnknownMembers(true)); err != nil {
		return Receipt{}, fmt.Errorf("decode setup receipt: %w", err)
	}
	return receipt, nil
}

func (s *Store) LatestOwned(target Target) (Receipt, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return Receipt{}, fmt.Errorf("list setup receipts: %w", err)
	}
	var receipts []Receipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id, parseErr := uuid.Parse(strings.TrimSuffix(entry.Name(), ".json"))
		if parseErr != nil {
			continue
		}
		receipt, loadErr := s.Load(id)
		if loadErr != nil {
			return Receipt{}, loadErr
		}
		if receipt.Target == target && receipt.Status == ReceiptCommitted && len(receipt.OwnedComponents) > 0 {
			receipts = append(receipts, receipt)
		}
	}
	if len(receipts) == 0 {
		return Receipt{}, ErrReceiptNotFound
	}
	slices.SortFunc(receipts, func(left, right Receipt) int { return left.CreatedAt.Compare(right.CreatedAt) })
	return receipts[len(receipts)-1], nil
}
