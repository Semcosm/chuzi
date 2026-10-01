package store

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

const matrixSyncCursorKey = "default"

var ErrInvalidMatrixSyncCursor = errors.New("store: invalid Matrix sync cursor")

// GetMatrixSyncCursor returns the last next_batch committed after a complete
// Matrix sync batch. An empty cursor means the gateway has not committed one.
func (s *Store) GetMatrixSyncCursor() (string, error) {
	var cursor string
	err := s.view(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.MatrixSyncCursorsBucket))
		if bucket == nil {
			return fmt.Errorf("%w: missing cursor bucket", ErrCorruptData)
		}
		raw := bucket.Get([]byte(matrixSyncCursorKey))
		if raw == nil {
			return nil
		}
		cursor = string(raw)
		return validateMatrixSyncCursor(cursor)
	})
	if err != nil {
		return "", err
	}
	return cursor, nil
}

// SetMatrixSyncCursor atomically replaces the durable cursor. An empty value
// clears it, which is useful when intentionally starting a fresh Matrix sync.
func (s *Store) SetMatrixSyncCursor(cursor string) error {
	if err := validateMatrixSyncCursor(cursor); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.MatrixSyncCursorsBucket))
		if bucket == nil {
			return fmt.Errorf("%w: missing cursor bucket", ErrCorruptData)
		}
		if cursor == "" {
			return bucket.Delete([]byte(matrixSyncCursorKey))
		}
		return bucket.Put([]byte(matrixSyncCursorKey), []byte(cursor))
	})
}

func validateMatrixSyncCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if len(cursor) > 4096 || strings.TrimSpace(cursor) != cursor || !utf8.ValidString(cursor) || strings.IndexByte(cursor, 0) >= 0 {
		return ErrInvalidMatrixSyncCursor
	}
	return nil
}
