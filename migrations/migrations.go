// Package migrations owns the durable bbolt schema version and migrations.
//
// Migrations are deliberately small, repeatable, and independent from the
// application store so opening a database can validate its schema before any
// domain data is read.
package migrations

import (
	"encoding/binary"
	"errors"
	"fmt"

	"go.etcd.io/bbolt"
)

const (
	CurrentVersion uint64 = 3

	MetaBucket               = "meta"
	AccountsBucket           = "accounts"
	RequestsBucket           = "requests"
	RequestIdempotencyBucket = "request_idempotency"
	AuditsBucket             = "audits"
	EventsBucket             = "events"
	LeasesBucket             = "leases"
	QueueBucket              = "queue"
	CredentialsBucket        = "credentials"
	CredentialAuditsBucket   = "credential_audits"
	VersionKey               = "version"
)

var (
	ErrNilDatabase        = errors.New("migrations: database is nil")
	ErrInvalidVersion     = errors.New("migrations: invalid schema version")
	ErrUnsupportedVersion = errors.New("migrations: unsupported schema version")
)

// Apply creates the schema and advances it to CurrentVersion. Applying it
// repeatedly is safe and leaves an already-current database unchanged.
func Apply(db *bbolt.DB) error {
	if db == nil {
		return ErrNilDatabase
	}

	return db.Update(func(tx *bbolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists([]byte(MetaBucket))
		if err != nil {
			return fmt.Errorf("create metadata bucket: %w", err)
		}

		version, err := readVersion(meta)
		if err != nil {
			return err
		}
		if version > CurrentVersion {
			return fmt.Errorf("%w: %d is newer than %d", ErrUnsupportedVersion, version, CurrentVersion)
		}

		for version < CurrentVersion {
			switch version + 1 {
			case 1:
				if err := createVersionOne(tx); err != nil {
					return err
				}
			case 2:
				if err := createVersionTwo(tx); err != nil {
					return err
				}
			case 3:
				if err := createVersionThree(tx); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: migration %d", ErrUnsupportedVersion, version+1)
			}
			version++
			if err := writeVersion(meta, version); err != nil {
				return err
			}
		}
		return nil
	})
}

func createVersionTwo(tx *bbolt.Tx) error {
	if _, err := tx.CreateBucketIfNotExists([]byte(QueueBucket)); err != nil {
		return fmt.Errorf("create %s bucket: %w", QueueBucket, err)
	}
	return nil
}

func createVersionThree(tx *bbolt.Tx) error {
	for _, name := range []string{CredentialsBucket, CredentialAuditsBucket} {
		if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
			return fmt.Errorf("create %s bucket: %w", name, err)
		}
	}
	return nil
}

// Version returns the recorded schema version. A database without metadata
// reports version zero, which is useful for migration tests and diagnostics.
func Version(db *bbolt.DB) (uint64, error) {
	if db == nil {
		return 0, ErrNilDatabase
	}

	var version uint64
	err := db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket([]byte(MetaBucket))
		if meta == nil {
			return nil
		}
		var err error
		version, err = readVersion(meta)
		return err
	})
	return version, err
}

func createVersionOne(tx *bbolt.Tx) error {
	for _, name := range []string{
		AccountsBucket,
		RequestsBucket,
		RequestIdempotencyBucket,
		AuditsBucket,
		EventsBucket,
		LeasesBucket,
	} {
		if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
			return fmt.Errorf("create %s bucket: %w", name, err)
		}
	}
	return nil
}

func readVersion(meta *bbolt.Bucket) (uint64, error) {
	raw := meta.Get([]byte(VersionKey))
	if raw == nil {
		return 0, nil
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("%w: version value has length %d", ErrInvalidVersion, len(raw))
	}
	return binary.BigEndian.Uint64(raw), nil
}

func writeVersion(meta *bbolt.Bucket, version uint64) error {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, version)
	if err := meta.Put([]byte(VersionKey), raw); err != nil {
		return fmt.Errorf("write schema version %d: %w", version, err)
	}
	return nil
}
