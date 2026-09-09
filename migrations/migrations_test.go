package migrations

import (
	"fmt"
	"path/filepath"
	"testing"

	"go.etcd.io/bbolt"
)

func TestApplyIsRepeatableAndRecordsVersion(t *testing.T) {
	db, err := bbolt.Open(filepath.Join(t.TempDir(), "schema.db"), 0o600, &bbolt.Options{Timeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := Apply(db); err != nil {
		t.Fatal(err)
	}
	if err := Apply(db); err != nil {
		t.Fatal(err)
	}
	version, err := Version(db)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentVersion {
		t.Fatalf("schema version = %d, want %d", version, CurrentVersion)
	}

	err = db.View(func(tx *bbolt.Tx) error {
		for _, name := range []string{
			MetaBucket,
			AccountsBucket,
			RequestsBucket,
			RequestIdempotencyBucket,
			AuditsBucket,
			EventsBucket,
			LeasesBucket,
			QueueBucket,
			CredentialsBucket,
			CredentialAuditsBucket,
		} {
			if tx.Bucket([]byte(name)) == nil {
				t.Errorf("bucket %q is missing", name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestApplyUpgradesVersionTwoWithCredentialBuckets(t *testing.T) {
	db, err := bbolt.Open(filepath.Join(t.TempDir(), "schema-v2.db"), 0o600, &bbolt.Options{Timeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(func(tx *bbolt.Tx) error {
		meta, err := tx.CreateBucket([]byte(MetaBucket))
		if err != nil {
			return err
		}
		if err := writeVersion(meta, 2); err != nil {
			return err
		}
		for _, name := range []string{AccountsBucket, RequestsBucket, RequestIdempotencyBucket, AuditsBucket, EventsBucket, LeasesBucket, QueueBucket} {
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(db); err != nil {
		t.Fatal(err)
	}
	if version, err := Version(db); err != nil || version != CurrentVersion {
		t.Fatalf("upgraded version = %d, %v", version, err)
	}
	if err := db.View(func(tx *bbolt.Tx) error {
		if tx.Bucket([]byte(CredentialsBucket)) == nil || tx.Bucket([]byte(CredentialAuditsBucket)) == nil {
			return fmt.Errorf("credential buckets missing after v2 upgrade")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
