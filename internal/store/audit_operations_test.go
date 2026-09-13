package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

func TestGlobalAuditViewIsRedactedAndBounded(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-private"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-private", "account-private", "idem-private", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-private", "account-private", "request-private", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	entries, err := service.ListAuditEntries(AuditQuery{Limit: 10})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	if entries[0].Account == "account-private" || entries[0].AuditID == "event-private" || entries[0].Actor == "test" || entries[0].RequestID == "request-private" {
		t.Fatalf("audit leaked raw values = %#v", entries[0])
	}
	if _, err := service.ListAuditEntries(AuditQuery{Limit: 10001}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid limit error = %v", err)
	}
}

func TestGlobalAuditViewIncludesCredentialMetadataWithoutSecrets(t *testing.T) {
	service, _ := openTestStore(t)
	accountID := "account-private"
	if _, err := service.CreateAccount(accountID); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-private", accountID, "idem-private", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-private", accountID, request.RequestID, account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	key, err := credential.NewKey("key-private", bytesForStore(0x55, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := credential.NewStaticKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := credential.New(service, keyring)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Put(context.Background(), accountID, []byte("credential-secret"), "actor-private", storeTestTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	entries, err := service.ListAuditEntries(AuditQuery{Limit: 10})
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, secret := range []string{accountID, request.RequestID, "actor-private", "key-private", "credential-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("global audit leaked %q: %s", secret, output)
		}
	}
	if entries[0].Kind != StateAudit || entries[1].Kind != CredentialAudit || entries[1].Version != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	limited, err := service.ListAuditEntries(AuditQuery{Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].Kind != StateAudit {
		t.Fatalf("bounded entries = %#v, %v", limited, err)
	}
}

func TestOperationalSnapshotCountsExpiredWork(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-1", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime)
	if _, err := service.AcquireLease("account-1", storeTestTime, "lease-1", "worker", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HeartbeatLease("account-1", storeTestTime, "lease-1", "worker", time.Second); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.OperationalSnapshot(storeTestTime.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Accounts != 1 || snapshot.Requests != 1 || snapshot.QueuedRequests != 1 || snapshot.ExpiredLeases != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestValidateDatabaseAndBackupRejectCorruption(t *testing.T) {
	service, _ := openTestStore(t)
	if err := service.ValidateDatabase(); err != nil {
		t.Fatal(err)
	}
	path, err := service.Backup(storeTestTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDatabaseKeepsHistoricalNotificationsAfterRetry(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-retry"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-retry", "account-retry", "idem-retry", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-queued", request.AccountID, request.RequestID, account.NoRequest, account.Queued, storeTestTime)
	applyEvent(t, service, "event-starting", request.AccountID, request.RequestID, account.Queued, account.Starting, storeTestTime.Add(time.Second))
	applyEvent(t, service, "event-logging", request.AccountID, request.RequestID, account.Starting, account.LoggingIn, storeTestTime.Add(2*time.Second))
	state, err := service.GetAccount(request.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	failure := account.Event{
		EventID:          "event-failed",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.LoggingIn,
		ExpectedRevision: state.Revision,
		To:               account.LoginFailed,
		Reason:           "transient failure",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(3 * time.Second),
	}
	retry := account.Event{
		EventID:          "event-retry",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.LoginFailed,
		ExpectedRevision: state.Revision + 1,
		To:               account.Queued,
		Reason:           "retry",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(3 * time.Second),
	}
	if _, err := service.RecordFailure(failure, account.TransientFailure, storeTestTime.Add(time.Minute), &retry); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateDatabase(); err != nil {
		t.Fatalf("historical notification made database invalid: %v", err)
	}
	backup, err := service.Backup(storeTestTime.Add(2 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(backup); err != nil {
		t.Fatalf("backup with historical notification is invalid: %v", err)
	}
}

func TestValidateBackupRejectsCorruptionAndRestoreLeavesActiveDatabase(t *testing.T) {
	service, cfg := openTestStore(t)
	if _, err := service.CreateAccount("active-account"); err != nil {
		t.Fatal(err)
	}
	backupPath, err := service.Backup(storeTestTime.Add(3 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	backup, err := bbolt.Open(backupPath, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = backup.Update(func(tx *bbolt.Tx) error {
		return tx.DeleteBucket([]byte(migrations.QueueBucket))
	})
	if closeErr := backup.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBackup(backupPath); !errors.Is(err, ErrInvalidRestore) {
		t.Fatalf("corrupt backup validation = %v, want ErrInvalidRestore", err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(cfg, backupPath); !errors.Is(err, ErrInvalidRestore) {
		t.Fatalf("corrupt restore = %v, want ErrInvalidRestore", err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.GetAccount("active-account"); err != nil {
		t.Fatalf("active database changed after rejected restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "chuzi.db")); err != nil {
		t.Fatalf("active database missing after rejected restore: %v", err)
	}
}
