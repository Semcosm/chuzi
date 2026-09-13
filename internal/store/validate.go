package store

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

// ValidateDatabase checks schema and every persisted projection without
// mutating the database. It is used before installing a restore and can also
// be run as a periodic diagnostic against the active store.
func (s *Store) ValidateDatabase() error {
	if s == nil || s.db == nil {
		return bbolt.ErrDatabaseNotOpen
	}
	return validateDB(s.db)
}

// ValidateBackup validates a regular owner-only bbolt backup in place. It does
// not require the backup to live under a service data directory, so callers
// can use it for an offline recovery drill after copying a file explicitly.
func ValidateBackup(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return ErrInvalidRestore
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidRestore
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return ErrInvalidRestore
	}
	database, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("%w: open backup: %v", ErrInvalidRestore, err)
	}
	defer database.Close()
	if err := validateDB(database); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRestore, err)
	}
	return nil
}

// ValidateRestore is an alias kept for callers that use the maintenance
// operation name rather than the backup artifact name.
func ValidateRestore(path string) error { return ValidateBackup(path) }

func validateDB(database *bbolt.DB) error {
	if database == nil {
		return bbolt.ErrDatabaseNotOpen
	}
	version, err := migrations.Version(database)
	if err != nil {
		return err
	}
	if version != migrations.CurrentVersion {
		return fmt.Errorf("%w: backup schema is not current", ErrInvalidRestore)
	}
	return database.View(func(tx *bbolt.Tx) error {
		for _, name := range []string{
			migrations.MetaBucket, migrations.AccountsBucket, migrations.RequestsBucket,
			migrations.RequestIdempotencyBucket, migrations.AuditsBucket,
			migrations.EventsBucket, migrations.LeasesBucket, migrations.QueueBucket,
			migrations.CredentialsBucket, migrations.CredentialAuditsBucket,
			migrations.MatrixNotificationsBucket,
		} {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("%w: required bucket %q is missing", ErrCorruptData, name)
			}
		}
		if err := validateAccountsTx(tx); err != nil {
			return err
		}
		if err := validateRequestsTx(tx); err != nil {
			return err
		}
		if err := validateIdempotencyTx(tx); err != nil {
			return err
		}
		if err := validateLeasesTx(tx); err != nil {
			return err
		}
		if err := validateQueueTx(tx); err != nil {
			return err
		}
		if err := validateCredentialsTx(tx); err != nil {
			return err
		}
		if err := validateNotificationsTx(tx); err != nil {
			return err
		}
		return validateEventsTx(tx)
	})
}

func validateAccountsTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.AccountsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		if _, err := snapshotFromTx(tx, string(key)); err != nil {
			return err
		}
		return nil
	})
}

func validateRequestsTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.RequestsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		request, err := requestFromTx(tx, string(key))
		if err != nil {
			return err
		}
		if request.RequestID != string(key) {
			return fmt.Errorf("%w: request key mismatch", ErrCorruptData)
		}
		if _, err := snapshotFromTx(tx, request.AccountID); err != nil {
			return err
		}
		return nil
	})
}

func validateIdempotencyTx(tx *bbolt.Tx) error {
	requests := tx.Bucket([]byte(migrations.RequestsBucket))
	return tx.Bucket([]byte(migrations.RequestIdempotencyBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		requestID := string(value)
		raw := requests.Get(value)
		if raw == nil {
			return fmt.Errorf("%w: idempotency key %q points to missing request", ErrCorruptData, string(key))
		}
		var request Request
		if err := decode(raw, &request); err != nil {
			return err
		}
		if request.RequestID != requestID || request.IdempotencyKey != string(key) {
			return fmt.Errorf("%w: idempotency index mismatch", ErrCorruptData)
		}
		return nil
	})
}

func validateLeasesTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.LeasesBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		if _, err := snapshotFromTx(tx, string(key)); err != nil {
			return err
		}
		if _, _, err := leaseFromTx(tx, string(key)); err != nil {
			return err
		}
		return nil
	})
}

func validateQueueTx(tx *bbolt.Tx) error {
	queue := tx.Bucket([]byte(migrations.QueueBucket))
	requests := tx.Bucket([]byte(migrations.RequestsBucket))
	seen := make(map[string]bool)
	if err := queue.ForEach(func(key, value []byte) error {
		if value == nil || len(key) < 10 || string(value) == "" {
			return fmt.Errorf("%w: invalid queue entry", ErrCorruptData)
		}
		requestID := string(value)
		if seen[requestID] {
			return fmt.Errorf("%w: duplicate queue request", ErrCorruptData)
		}
		seen[requestID] = true
		raw := requests.Get(value)
		if raw == nil {
			return fmt.Errorf("%w: queue points to missing request", ErrCorruptData)
		}
		var request Request
		if err := decode(raw, &request); err != nil {
			return err
		}
		if request.State != account.Queued || request.RequestID != requestID || string(key) != string(queueKey(request.CreatedAt, request.RequestID)) {
			return fmt.Errorf("%w: queue index mismatch", ErrCorruptData)
		}
		return nil
	}); err != nil {
		return err
	}
	return requests.ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		var request Request
		if err := decode(value, &request); err != nil {
			return err
		}
		if request.State == account.Queued && !seen[request.RequestID] {
			return fmt.Errorf("%w: queued request is absent from index", ErrCorruptData)
		}
		return nil
	})
}

func validateCredentialsTx(tx *bbolt.Tx) error {
	accounts := tx.Bucket([]byte(migrations.AccountsBucket))
	if err := tx.Bucket([]byte(migrations.CredentialsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		var record credential.Record
		if err := decode(value, &record); err != nil {
			return err
		}
		if record.AccountID != string(key) {
			return fmt.Errorf("%w: credential key mismatch", ErrCorruptData)
		}
		if accounts.Get(key) == nil {
			return fmt.Errorf("%w: credential references missing account", ErrCorruptData)
		}
		if err := record.Validate(); err != nil {
			return fmt.Errorf("%w: invalid credential record", ErrCorruptData)
		}
		return nil
	}); err != nil {
		return err
	}
	return tx.Bucket([]byte(migrations.CredentialAuditsBucket)).ForEach(func(key, value []byte) error {
		if value != nil {
			return fmt.Errorf("%w: credential audit root contains a value", ErrCorruptData)
		}
		audits := tx.Bucket([]byte(migrations.CredentialAuditsBucket)).Bucket(key)
		if audits == nil {
			return fmt.Errorf("%w: credential audit bucket is missing", ErrCorruptData)
		}
		if accounts.Get(key) == nil {
			return fmt.Errorf("%w: credential audit references missing account", ErrCorruptData)
		}
		return audits.ForEach(func(auditID, raw []byte) error {
			if raw == nil {
				return nil
			}
			var audit credential.Audit
			if err := decode(raw, &audit); err != nil {
				return err
			}
			if string(key) != audit.AccountID || string(auditID) != audit.AuditID {
				return fmt.Errorf("%w: credential audit key mismatch", ErrCorruptData)
			}
			if err := audit.Validate(); err != nil {
				return fmt.Errorf("%w: invalid credential audit", ErrCorruptData)
			}
			return nil
		})
	})
}

func validateNotificationsTx(tx *bbolt.Tx) error {
	accounts := tx.Bucket([]byte(migrations.AccountsBucket))
	events := tx.Bucket([]byte(migrations.EventsBucket))
	return tx.Bucket([]byte(migrations.MatrixNotificationsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		var notification Notification
		if err := decode(value, &notification); err != nil {
			return err
		}
		if string(key) != notification.EventID {
			return fmt.Errorf("%w: notification key mismatch", ErrCorruptData)
		}
		if err := notification.Validate(); err != nil {
			return fmt.Errorf("%w: invalid notification", ErrCorruptData)
		}
		if accounts.Get([]byte(notification.AccountID)) == nil {
			return fmt.Errorf("%w: notification references missing account", ErrCorruptData)
		}
		request, err := requestFromTx(tx, notification.RequestID)
		if err != nil {
			return fmt.Errorf("%w: notification request is missing", ErrCorruptData)
		}
		// Notifications are historical event projections. The request may have
		// advanced through later states (for example LOGIN_FAILED -> QUEUED)
		// while the earlier notification remains pending or already delivered.
		// Validate identity here and compare the historical state below against
		// the immutable event instead of the current request state.
		if request.AccountID != notification.AccountID {
			return fmt.Errorf("%w: notification/request projection mismatch", ErrCorruptData)
		}
		rawEvent := events.Get([]byte(notification.EventID))
		if rawEvent == nil {
			return fmt.Errorf("%w: notification event is missing", ErrCorruptData)
		}
		var audit account.AuditRecord
		if err := decode(rawEvent, &audit); err != nil {
			return err
		}
		if audit.Event.AccountID != notification.AccountID || audit.Event.RequestID != notification.RequestID || audit.Event.To != notification.State || !audit.Event.OccurredAt.Equal(notification.OccurredAt) {
			return fmt.Errorf("%w: notification/event projection mismatch", ErrCorruptData)
		}
		return nil
	})
}

func validateEventsTx(tx *bbolt.Tx) error {
	audits := tx.Bucket([]byte(migrations.AuditsBucket))
	accounts := tx.Bucket([]byte(migrations.AccountsBucket))
	return tx.Bucket([]byte(migrations.EventsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		var audit account.AuditRecord
		if err := decode(value, &audit); err != nil {
			return err
		}
		if string(key) != audit.Event.EventID {
			return fmt.Errorf("%w: event key mismatch", ErrCorruptData)
		}
		if accounts.Get([]byte(audit.Event.AccountID)) == nil {
			return fmt.Errorf("%w: event references missing account", ErrCorruptData)
		}
		accountAudits := audits.Bucket([]byte(audit.Event.AccountID))
		if accountAudits == nil {
			return fmt.Errorf("%w: event references missing audit bucket", ErrCorruptData)
		}
		rawAudit := accountAudits.Get(revisionKey(audit.Revision))
		if rawAudit == nil {
			return fmt.Errorf("%w: event references missing audit revision", ErrCorruptData)
		}
		var indexed account.AuditRecord
		if err := decode(rawAudit, &indexed); err != nil {
			return err
		}
		if !auditRecordsEqual(audit, indexed) {
			return fmt.Errorf("%w: event/audit projection mismatch", ErrCorruptData)
		}
		return nil
	})
}
