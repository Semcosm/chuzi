package migrations

import (
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
