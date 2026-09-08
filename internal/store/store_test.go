package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var storeTestTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) (*Store, config.Config) {
	t.Helper()
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	return service, cfg
}

func createRequest(t *testing.T, service *Store, requestID, accountID, idempotencyKey string, createdAt time.Time) Request {
	t.Helper()
	request, err := NewRequest(requestID, accountID, idempotencyKey, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func applyEvent(t *testing.T, service *Store, eventID, accountID, requestID string, from, to account.Status, occurredAt time.Time) account.TransitionResult {
	t.Helper()
	state, err := service.GetAccount(accountID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyEvent(account.Event{
		EventID:          eventID,
		AccountID:        accountID,
		RequestID:        requestID,
		From:             from,
		ExpectedRevision: state.Revision,
		To:               to,
		Reason:           "store test",
		Actor:            "test",
		OccurredAt:       occurredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStorePersistsStateAndRecoversAfterRestart(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	created, idempotent, err := service.CreateRequest(request)
	if err != nil || idempotent || created != request {
		t.Fatalf("CreateRequest() = %#v, %t, %v", created, idempotent, err)
	}
	applyEvent(t, service, "event-1", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	applyEvent(t, service, "event-2", "account-1", "request-1", account.Queued, account.Starting, storeTestTime.Add(2*time.Second))
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	version, err := restarted.SchemaVersion()
	if err != nil || version != migrations.CurrentVersion {
		t.Fatalf("SchemaVersion() = %d, %v; want %d", version, err, migrations.CurrentVersion)
	}
	state, err := restarted.GetAccount("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != account.Starting || state.RequestID != "request-1" || state.Revision != 2 {
		t.Fatalf("recovered account = %#v", state)
	}
	recoveredRequest, err := restarted.GetRequest("request-1")
	if err != nil {
		t.Fatal(err)
	}
	if recoveredRequest.State != account.Starting || !recoveredRequest.UpdatedAt.Equal(storeTestTime.Add(2*time.Second)) {
		t.Fatalf("recovered request = %#v", recoveredRequest)
	}
	audits, err := restarted.GetAudits("account-1")
	if err != nil || len(audits) != 2 || audits[0].Revision != 1 || audits[1].Revision != 2 {
		t.Fatalf("recovered audits = %#v, %v", audits, err)
	}
	audit, err := restarted.GetAudit("account-1", 2)
	if err != nil || audit.Event.EventID != "event-2" {
		t.Fatalf("GetAudit() = %#v, %v", audit, err)
	}
}

func TestStoreRequestIdempotencyUsesImmutableIdentityAfterStateChanges(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-1", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))

	retry, idempotent, err := service.CreateRequest(request)
	if err != nil || !idempotent || retry.State != account.Queued {
		t.Fatalf("stateful idempotent retry = %#v, %t, %v", retry, idempotent, err)
	}

	conflict := request
	conflict.RequestID = "request-2"
	if _, _, err := service.CreateRequest(conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting idempotency request error = %v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := service.CreateRequest(Request{
		RequestID:      request.RequestID,
		AccountID:      request.AccountID,
		IdempotencyKey: request.IdempotencyKey + "-other",
		State:          account.NoRequest,
		CreatedAt:      request.CreatedAt,
		UpdatedAt:      request.UpdatedAt,
	}); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("conflicting request id error = %v, want ErrRequestConflict", err)
	}
}

func TestStoreDuplicateAndConflictingEvents(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	event := account.Event{
		EventID:    "event-1",
		AccountID:  "account-1",
		RequestID:  "request-1",
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "store test",
		Actor:      "test",
		OccurredAt: storeTestTime.Add(time.Second),
	}
	first, err := service.ApplyEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.ApplyEvent(event)
	if err != nil || !duplicate.Idempotent || duplicate.Audit != first.Audit || duplicate.State.Revision != 1 {
		t.Fatalf("duplicate event = %#v, %v; first = %#v", duplicate, err, first)
	}
	conflict := event
	conflict.Reason = "different"
	if _, err := service.ApplyEvent(conflict); !errors.Is(err, account.ErrEventConflict) {
		t.Fatalf("conflicting event error = %v, want ErrEventConflict", err)
	}
	audits, err := service.GetAudits("account-1")
	if err != nil || len(audits) != 1 {
		t.Fatalf("audit count after duplicate/conflict = %d, %v", len(audits), err)
	}
}

func TestStoreFailedEventLeavesNoPartialWrite(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	badEvent := account.Event{
		EventID:          "event-failed",
		AccountID:        "account-1",
		RequestID:        "request-1",
		From:             account.Queued,
		ExpectedRevision: 0,
		To:               account.Starting,
		Reason:           "invalid precondition",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(time.Second),
	}
	if _, err := service.ApplyEvent(badEvent); !errors.Is(err, account.ErrStaleEvent) {
		t.Fatalf("failed event error = %v, want ErrStaleEvent", err)
	}
	state, err := service.GetAccount("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != account.NoRequest || state.Revision != 0 || len(state.AppliedEvents) != 0 {
		t.Fatalf("account changed after failed event = %#v", state)
	}
	if _, err := service.GetAudit("account-1", 1); !errors.Is(err, ErrAuditNotFound) {
		t.Fatalf("failed event audit lookup = %v, want ErrAuditNotFound", err)
	}
	stored, err := service.GetRequest("request-1")
	if err != nil || stored.State != account.NoRequest {
		t.Fatalf("request changed after failed event = %#v, %v", stored, err)
	}
}

func TestStoreRollsBackWhenAuditWriteFailsAfterRequestUpdate(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	event := account.Event{
		EventID:    "event-1",
		AccountID:  "account-1",
		RequestID:  "request-1",
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "store test",
		Actor:      "test",
		OccurredAt: storeTestTime.Add(time.Second),
	}
	preexisting, err := json.Marshal(account.AuditRecord{Event: event, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.EventsBucket)).Put([]byte(event.EventID), preexisting)
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ApplyEvent(event); !errors.Is(err, account.ErrEventConflict) {
		t.Fatalf("audit write failure = %v, want ErrEventConflict", err)
	}
	state, err := service.GetAccount("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != account.NoRequest || state.Revision != 0 || len(state.AppliedEvents) != 0 {
		t.Fatalf("account changed after rolled-back event = %#v", state)
	}
	stored, err := service.GetRequest("request-1")
	if err != nil || stored.State != account.NoRequest {
		t.Fatalf("request changed after rolled-back event = %#v, %v", stored, err)
	}
	if _, err := service.GetAudit("account-1", 1); !errors.Is(err, ErrAuditNotFound) {
		t.Fatalf("rolled-back audit lookup = %v, want ErrAuditNotFound", err)
	}
}

func TestStoreLeasesCompetePersistAndRecover(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	first, err := service.AcquireLease("account-1", storeTestTime, "lease-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcquireLease("account-1", storeTestTime.Add(time.Second), "lease-2", "worker-2", time.Minute); !errors.Is(err, account.ErrLeaseHeld) {
		t.Fatalf("active lease competition error = %v, want ErrLeaseHeld", err)
	}
	if _, err := service.HeartbeatLease("account-1", storeTestTime.Add(10*time.Second), "lease-1", "worker-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(service.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	lease, exists, err := restarted.GetLease("account-1")
	if err != nil || !exists || lease.LeaseID != first.LeaseID {
		t.Fatalf("persisted lease = %#v, %t, %v", lease, exists, err)
	}
	recovered, err := restarted.AcquireLease("account-1", storeTestTime.Add(2*time.Minute), "lease-2", "worker-2", time.Minute)
	if err != nil || recovered.LeaseID != "lease-2" || recovered.Owner != "worker-2" {
		t.Fatalf("recovered lease = %#v, %v", recovered, err)
	}
	if err := restarted.ReleaseLease("account-1", "lease-2", "worker-2"); err != nil {
		t.Fatal(err)
	}
	_, exists, err = restarted.GetLease("account-1")
	if err != nil || exists {
		t.Fatalf("lease after release = exists=%t, err=%v", exists, err)
	}
}

func TestStoreLeaseCompetitionIsAtomic(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := service.AcquireLease("account-1", storeTestTime, "lease-"+string(rune('0'+i)), "worker-"+string(rune('0'+i)), time.Minute)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	var successes, held int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, account.ErrLeaseHeld):
			held++
		default:
			t.Fatalf("lease competition error = %v", err)
		}
	}
	if successes != 1 || held != 1 {
		t.Fatalf("lease competition outcomes = successes %d, held %d", successes, held)
	}
}

func TestStoreBackupCanBeReopenedAndRejectsDuplicatePath(t *testing.T) {
	service, cfg := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "event-1", "account-1", "request-1", account.NoRequest, account.Queued, storeTestTime.Add(time.Second))
	backupTime := storeTestTime.Add(time.Hour)
	backupPath, err := service.Backup(backupTime)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := cfg.BackupPath(backupTime)
	if err != nil || backupPath != wantPath {
		t.Fatalf("backup path = %q, %v; want %q", backupPath, err, wantPath)
	}
	if mode, err := os.Stat(backupPath); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("backup stat = mode %v, err %v; want 0600", mode, err)
	}
	if _, err := service.Backup(backupTime); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("duplicate backup error = %v, want ErrBackupExists", err)
	}

	backupDB, err := bbolt.Open(backupPath, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	version, err := migrations.Version(backupDB)
	if err != nil || version != migrations.CurrentVersion {
		t.Fatalf("backup schema version = %d, %v", version, err)
	}
	if err := migrations.Apply(backupDB); err != nil {
		t.Fatal(err)
	}
}
