package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

func environmentKey(id, version string) string { return id + "@" + version }

// PutEnvironmentRecord persists the lifecycle gates independently from slot
// state. Callers cannot set Ready without the complete verified/trusted/
// enabled/healthy chain because Record.Validate enforces that invariant.
func (s *Store) PutEnvironmentRecord(record environment.Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		return putJSON(tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)), environmentKey(record.EnvironmentID, record.Version), record)
	})
}

// DeleteEnvironmentRecord removes only the durable projection. The filesystem
// package is owned by environment.Manager and must be removed through that
// boundary first.
func (s *Store) DeleteEnvironmentRecord(environmentID, version string) error {
	if strings.TrimSpace(environmentID) == "" || strings.TrimSpace(version) == "" {
		return environment.ErrInvalidManifest
	}
	return s.update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).Delete([]byte(environmentKey(environmentID, version)))
	})
}

func (s *Store) GetEnvironmentRecord(environmentID, version string) (environment.Record, error) {
	var result environment.Record
	err := s.view(func(tx *bbolt.Tx) error {
		if err := getJSON(tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)), environmentKey(environmentID, version), &result, fmt.Errorf("environment record not found")); err != nil {
			return err
		}
		if err := result.Validate(); err != nil {
			return fmt.Errorf("%w: invalid environment record", ErrCorruptData)
		}
		return nil
	})
	return result, err
}

// validatePoolEnvironmentTx applies the durable environment gate inside the
// same transaction that creates a slot lease. Older low-level stores may have
// no environment records at all, so they retain their compatibility behavior;
// once the lifecycle bucket is populated, every metadata-bearing pool must
// resolve to a ready record before it can be selected.
func validatePoolEnvironmentTx(tx *bbolt.Tx, config slot.PoolConfig) error {
	bucket := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket))
	if bucket == nil || bucket.Stats().KeyN == 0 {
		return nil
	}
	if strings.TrimSpace(config.EnvironmentVersion) == "" || strings.TrimSpace(config.ManifestDigest) == "" || strings.TrimSpace(config.Signer) == "" {
		return ErrEnvironmentUnavailable
	}
	raw := bucket.Get([]byte(environmentKey(config.EnvironmentID, config.EnvironmentVersion)))
	if raw == nil {
		return ErrEnvironmentUnavailable
	}
	var record environment.Record
	if err := decode(raw, &record); err != nil {
		return fmt.Errorf("%w: invalid environment record", ErrCorruptData)
	}
	if err := record.Validate(); err != nil || !record.IsReady() || record.EnvironmentID != config.EnvironmentID || record.Version != config.EnvironmentVersion || !strings.EqualFold(record.ManifestDigest, config.ManifestDigest) || record.Signer != config.Signer || !containsCapabilities(record.Capabilities, config.Capabilities) {
		return ErrEnvironmentUnavailable
	}
	return nil
}

func (s *Store) ListEnvironmentRecords() ([]environment.Record, error) {
	result := make([]environment.Record, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return fmt.Errorf("%w: environment package bucket contains nested bucket", ErrCorruptData)
			}
			var record environment.Record
			if err := decode(raw, &record); err != nil {
				return err
			}
			if string(key) != environmentKey(record.EnvironmentID, record.Version) || strings.ContainsRune(string(key), '\x00') {
				return fmt.Errorf("%w: environment record key mismatch", ErrCorruptData)
			}
			if err := record.Validate(); err != nil {
				return fmt.Errorf("%w: invalid environment record", ErrCorruptData)
			}
			result = append(result, record)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool {
		if result[i].EnvironmentID != result[j].EnvironmentID {
			return result[i].EnvironmentID < result[j].EnvironmentID
		}
		return result[i].Version < result[j].Version
	})
	return result, err
}
