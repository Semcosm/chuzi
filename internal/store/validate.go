package store

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
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
			migrations.MatrixNotificationsBucket, migrations.AccountDeletionsBucket,
			migrations.MatrixSyncCursorsBucket, migrations.JobPoolsBucket,
			migrations.ExecutionSlotsBucket, migrations.SlotLeasesBucket,
			migrations.EnvironmentSummariesBucket,
			migrations.EnvironmentPackagesBucket,
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
		if err := validateDeletionsTx(tx); err != nil {
			return err
		}
		if err := validateMatrixSyncCursorTx(tx); err != nil {
			return err
		}
		if err := validateJobPoolsTx(tx); err != nil {
			return err
		}
		if err := validateSlotsTx(tx); err != nil {
			return err
		}
		if err := validateEnvironmentSummariesTx(tx); err != nil {
			return err
		}
		if err := validateEnvironmentPackagesTx(tx); err != nil {
			return err
		}
		return validateEventsTx(tx)
	})
}

func validateEnvironmentPackagesTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.EnvironmentPackagesBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return fmt.Errorf("%w: environment package bucket contains nested bucket", ErrCorruptData)
		}
		var record environment.Record
		if err := decode(value, &record); err != nil {
			return err
		}
		if string(key) != record.EnvironmentID+"@"+record.Version {
			return fmt.Errorf("%w: environment package key mismatch", ErrCorruptData)
		}
		if err := record.Validate(); err != nil {
			return fmt.Errorf("%w: invalid environment package", ErrCorruptData)
		}
		return nil
	})
}

func validateEnvironmentSummariesTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.EnvironmentSummariesBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return fmt.Errorf("%w: environment summary bucket contains nested bucket", ErrCorruptData)
		}
		var summary slot.EnvironmentSummary
		if err := decode(value, &summary); err != nil {
			return err
		}
		if string(key) != summary.EnvironmentID {
			return fmt.Errorf("%w: environment summary key mismatch", ErrCorruptData)
		}
		if err := summary.Validate(); err != nil {
			return fmt.Errorf("%w: invalid environment summary", ErrCorruptData)
		}
		return nil
	})
}

func validateJobPoolsTx(tx *bbolt.Tx) error {
	environmentPackages := tx.Bucket([]byte(migrations.EnvironmentPackagesBucket))
	return tx.Bucket([]byte(migrations.JobPoolsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return fmt.Errorf("%w: job pool bucket contains nested bucket", ErrCorruptData)
		}
		var config slot.PoolConfig
		if err := decode(value, &config); err != nil {
			return err
		}
		if string(key) != config.PoolID {
			return fmt.Errorf("%w: job pool key mismatch", ErrCorruptData)
		}
		if err := config.Validate(); err != nil {
			return fmt.Errorf("%w: invalid job pool", ErrCorruptData)
		}
		// Once the environment registry contains records, every persisted pool
		// must resolve to a complete ready record. This keeps restore validation
		// from accepting a pool whose metadata no longer names a trusted package.
		if environmentPackages != nil && environmentPackages.Stats().KeyN > 0 {
			if err := validatePoolEnvironmentTx(tx, config); err != nil {
				return fmt.Errorf("%w: job pool environment is unavailable", ErrCorruptData)
			}
		}
		return nil
	})
}

func validateSlotsTx(tx *bbolt.Tx) error {
	slots := tx.Bucket([]byte(migrations.ExecutionSlotsBucket))
	leases := tx.Bucket([]byte(migrations.SlotLeasesBucket))
	jobPools := tx.Bucket([]byte(migrations.JobPoolsBucket))
	leaseOwners := make(map[string]string)
	if err := slots.ForEach(func(key, value []byte) error {
		if value == nil {
			return fmt.Errorf("%w: execution slot bucket contains nested bucket", ErrCorruptData)
		}
		var item slot.Slot
		if err := decode(value, &item); err != nil {
			return err
		}
		if string(key) != item.SlotID {
			return fmt.Errorf("%w: execution slot key mismatch", ErrCorruptData)
		}
		if err := item.Validate(); err != nil {
			return fmt.Errorf("%w: invalid execution slot", ErrCorruptData)
		}
		poolRaw := jobPools.Get([]byte(item.PoolID))
		if poolRaw == nil {
			return fmt.Errorf("%w: execution slot references missing pool", ErrCorruptData)
		}
		var pool slot.PoolConfig
		if err := decode(poolRaw, &pool); err != nil {
			return err
		}
		if err := pool.Validate(); err != nil {
			return fmt.Errorf("%w: execution slot references invalid pool", ErrCorruptData)
		}
		// Active leases and transitional states may retain the previous
		// environment until reconcile drains or reprovisions them. A Ready slot
		// must always match the pool's persisted target.
		if item.Status == slot.Ready && (item.EnvironmentID != pool.EnvironmentID || item.EnvironmentVersion != pool.EnvironmentVersion || item.ManifestDigest != pool.ManifestDigest || item.Signer != pool.Signer || !containsCapabilities(item.Capabilities, pool.Capabilities)) {
			return fmt.Errorf("%w: execution slot environment does not match pool", ErrCorruptData)
		}
		if item.Status == slot.Ready && !item.Trusted {
			return fmt.Errorf("%w: ready execution slot is not trusted", ErrCorruptData)
		}
		if (item.Status == slot.Leased || item.Status == slot.Draining) && leases.Get(key) == nil {
			return fmt.Errorf("%w: leased slot has no lease", ErrCorruptData)
		}
		return nil
	}); err != nil {
		return err
	}
	return leases.ForEach(func(key, value []byte) error {
		if value == nil {
			return fmt.Errorf("%w: slot lease bucket contains nested bucket", ErrCorruptData)
		}
		var lease slot.Lease
		if err := decode(value, &lease); err != nil {
			return err
		}
		if string(key) != lease.SlotID {
			return fmt.Errorf("%w: slot lease key mismatch", ErrCorruptData)
		}
		if err := lease.Validate(); err != nil {
			return fmt.Errorf("%w: invalid slot lease", ErrCorruptData)
		}
		raw := slots.Get(key)
		if raw == nil {
			return fmt.Errorf("%w: slot lease references missing slot", ErrCorruptData)
		}
		var item slot.Slot
		if err := decode(raw, &item); err != nil {
			return err
		}
		if lease.PoolID != item.PoolID || lease.EnvironmentGeneration != item.EnvironmentGeneration {
			return fmt.Errorf("%w: slot lease metadata does not match slot", ErrCorruptData)
		}
		var pool slot.PoolConfig
		if err := decode(jobPools.Get([]byte(lease.PoolID)), &pool); err != nil {
			return fmt.Errorf("%w: slot lease references invalid pool", ErrCorruptData)
		}
		if pool.PoolID != lease.PoolID {
			return fmt.Errorf("%w: slot lease references missing pool", ErrCorruptData)
		}
		if item.Status != slot.Leased && item.Status != slot.Draining {
			return fmt.Errorf("%w: slot lease references non-leased slot", ErrCorruptData)
		}
		if err := validateSlotLeaseOwnershipTx(tx, lease); err != nil {
			return err
		}
		ownerKey := lease.AccountID + "\x00" + lease.RequestID
		if previousSlot, exists := leaseOwners[ownerKey]; exists && previousSlot != lease.SlotID {
			return fmt.Errorf("%w: account/request has multiple slot leases", ErrCorruptData)
		}
		leaseOwners[ownerKey] = lease.SlotID
		return nil
	})
}

func validateSlotLeaseOwnershipTx(tx *bbolt.Tx, lease slot.Lease) error {
	snapshot, err := snapshotFromTx(tx, lease.AccountID)
	if err != nil {
		return fmt.Errorf("%w: slot lease account is missing", ErrCorruptData)
	}
	if snapshot.Status != account.Starting && snapshot.Status != account.LoggingIn {
		return fmt.Errorf("%w: slot lease account is not running", ErrCorruptData)
	}
	request, err := requestFromTx(tx, lease.RequestID)
	if err != nil {
		return fmt.Errorf("%w: slot lease request is missing", ErrCorruptData)
	}
	if request.AccountID != lease.AccountID || request.RequestID != lease.RequestID || request.State != snapshot.Status {
		return fmt.Errorf("%w: slot lease request/account projection mismatch", ErrCorruptData)
	}
	accountLease, exists, err := leaseFromTx(tx, lease.AccountID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: slot lease has no account lease", ErrCorruptData)
	}
	if accountLease.LeaseID+"-slot" != lease.LeaseID || accountLease.Owner != lease.Owner ||
		!accountLease.AcquiredAt.Equal(lease.AcquiredAt) || !accountLease.ExpiresAt.Equal(lease.ExpiresAt) {
		return fmt.Errorf("%w: slot lease does not match account lease", ErrCorruptData)
	}
	return nil
}

func validateDeletionsTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.AccountDeletionsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return fmt.Errorf("%w: deletion bucket contains nested bucket", ErrCorruptData)
		}
		var deletion account.DeletionSnapshot
		if err := decode(value, &deletion); err != nil {
			return err
		}
		if string(key) != deletion.AccountID {
			return fmt.Errorf("%w: deletion account key mismatch", ErrCorruptData)
		}
		if err := deletion.Validate(); err != nil {
			return fmt.Errorf("%w: invalid deletion record", ErrCorruptData)
		}
		if tx.Bucket([]byte(migrations.AccountsBucket)).Get(key) == nil {
			return fmt.Errorf("%w: deletion references missing account", ErrCorruptData)
		}
		return nil
	})
}

func validateMatrixSyncCursorTx(tx *bbolt.Tx) error {
	bucket := tx.Bucket([]byte(migrations.MatrixSyncCursorsBucket))
	return bucket.ForEach(func(key, value []byte) error {
		if string(key) != matrixSyncCursorKey || len(value) == 0 {
			return fmt.Errorf("%w: invalid Matrix sync cursor record", ErrCorruptData)
		}
		return validateMatrixSyncCursor(string(value))
	})
}

func validateAccountsTx(tx *bbolt.Tx) error {
	return tx.Bucket([]byte(migrations.AccountsBucket)).ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		snapshot, err := snapshotFromTx(tx, string(key))
		if err != nil {
			return err
		}
		if snapshot.Status == account.NoRequest {
			return nil
		}
		request, err := requestFromTx(tx, snapshot.RequestID)
		if err != nil {
			return fmt.Errorf("%w: account request is missing", ErrCorruptData)
		}
		if request.AccountID != snapshot.AccountID || request.RequestID != snapshot.RequestID || request.State != snapshot.Status {
			return fmt.Errorf("%w: account/request projection mismatch", ErrCorruptData)
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
		snapshot, err := snapshotFromTx(tx, request.AccountID)
		if err != nil {
			return err
		}
		if request.State == account.NoRequest {
			if snapshot.Status != account.NoRequest || snapshot.RequestID != "" {
				return fmt.Errorf("%w: initial request does not match account state", ErrCorruptData)
			}
		} else if snapshot.AccountID != request.AccountID || snapshot.RequestID != request.RequestID || snapshot.Status != request.State {
			return fmt.Errorf("%w: request/account projection mismatch", ErrCorruptData)
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
		accountID := string(key)
		snapshot, err := snapshotFromTx(tx, accountID)
		if err != nil {
			return err
		}
		lease, exists, err := leaseFromTx(tx, accountID)
		if err != nil {
			return err
		}
		if !exists || (snapshot.Status != account.Starting && snapshot.Status != account.LoggingIn) {
			return fmt.Errorf("%w: account lease has no active request", ErrCorruptData)
		}
		request, err := requestFromTx(tx, snapshot.RequestID)
		if err != nil || request.AccountID != accountID || request.RequestID != snapshot.RequestID || request.State != snapshot.Status {
			return fmt.Errorf("%w: account lease request projection mismatch", ErrCorruptData)
		}
		if lease.LeaseID == "" || lease.Owner == "" {
			return fmt.Errorf("%w: account lease identity is empty", ErrCorruptData)
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
