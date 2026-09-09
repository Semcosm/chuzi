package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	deadlineConflict := request
	deadlineConflict.Deadline = request.CreatedAt.Add(time.Minute)
	if _, _, err := service.CreateRequest(deadlineConflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different deadline error = %v, want ErrIdempotencyConflict", err)
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
	mode, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("backup stat: %v", err)
	}
	if !mode.Mode().IsRegular() {
		t.Fatalf("backup mode = %v; want a regular file", mode.Mode())
	}
	if runtime.GOOS != "windows" && mode.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions = %o; want 0600", mode.Mode().Perm())
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

func TestStoreSubmitRequestIsAtomicAndIdempotent(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAccount("account-2"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	event := account.Event{
		EventID:    "submit-1",
		AccountID:  request.AccountID,
		RequestID:  request.RequestID,
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "submit",
		Actor:      "test",
		OccurredAt: storeTestTime.Add(time.Second),
	}
	created, idempotent, err := service.SubmitRequest(request, event)
	if err != nil || idempotent || created.State != account.Queued {
		t.Fatalf("SubmitRequest() = %#v, %t, %v", created, idempotent, err)
	}
	repeated, idempotent, err := service.SubmitRequest(request, event)
	if err != nil || !idempotent || repeated.State != account.Queued {
		t.Fatalf("idempotent SubmitRequest() = %#v, %t, %v", repeated, idempotent, err)
	}
	laterRequest := request
	laterRequest.CreatedAt = request.CreatedAt.Add(time.Hour)
	laterRequest.UpdatedAt = laterRequest.CreatedAt
	laterRequest.NotBefore = laterRequest.CreatedAt
	later, idempotent, err := service.SubmitRequest(laterRequest, event)
	if err != nil || !idempotent || later.RequestID != request.RequestID || later.State != account.Queued {
		t.Fatalf("late idempotent SubmitRequest() = %#v, %t, %v", later, idempotent, err)
	}
	if state, err := service.GetAccount(request.AccountID); err != nil || state.Revision != 1 || state.Status != account.Queued {
		t.Fatalf("state after duplicate submit = %#v, %v", state, err)
	}

	failedRequest := createRequest(t, service, "request-2", "account-2", "idem-2", storeTestTime.Add(2*time.Second))
	badEvent := event
	badEvent.EventID = "submit-invalid"
	badEvent.RequestID = failedRequest.RequestID
	badEvent.AccountID = failedRequest.AccountID
	badEvent.ExpectedRevision = 99
	if _, _, err := service.SubmitRequest(failedRequest, badEvent); !errors.Is(err, account.ErrStaleEvent) {
		t.Fatalf("invalid submit error = %v, want stale event", err)
	}
	if _, err := service.GetRequest(failedRequest.RequestID); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("failed submit request lookup = %v, want ErrRequestNotFound", err)
	}
}

func TestStoreQueuedRequestsUseCreatedAtFIFOAndClaimLeaseAtomically(t *testing.T) {
	service, _ := openTestStore(t)
	for _, accountID := range []string{"account-a", "account-b"} {
		if _, err := service.CreateAccount(accountID); err != nil {
			t.Fatal(err)
		}
	}
	first := createRequest(t, service, "request-a", "account-a", "idem-a", storeTestTime)
	second := createRequest(t, service, "request-b", "account-b", "idem-b", storeTestTime)
	for _, submitted := range []struct {
		request Request
		eventID string
		at      time.Time
	}{
		{first, "submit-a", storeTestTime.Add(3 * time.Second)},
		{second, "submit-b", storeTestTime.Add(time.Second)},
	} {
		if _, _, err := service.SubmitRequest(submitted.request, account.Event{
			EventID:    submitted.eventID,
			AccountID:  submitted.request.AccountID,
			RequestID:  submitted.request.RequestID,
			From:       account.NoRequest,
			To:         account.Queued,
			Reason:     "submit",
			Actor:      "test",
			OccurredAt: submitted.at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := service.ListQueuedRequests(storeTestTime.Add(time.Minute))
	if err != nil || len(queued) != 2 || queued[0].RequestID != "request-a" || queued[1].RequestID != "request-b" {
		t.Fatalf("queued FIFO = %#v, %v", queued, err)
	}
	claim, err := service.ClaimNext(storeTestTime.Add(time.Minute), "lease-1", "worker-1", time.Minute, "claim-1", "worker-1", "claim", QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil || claim.Request.RequestID != "request-a" || claim.Request.Attempt != 1 || claim.Request.State != account.Starting {
		t.Fatalf("ClaimNext() = %#v, %v", claim, err)
	}
	duplicateClaim, err := service.ClaimNext(storeTestTime.Add(time.Minute), "lease-1", "worker-1", time.Minute, "claim-1", "worker-1", "claim", QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil || !duplicateClaim.Transition.Idempotent || duplicateClaim.Request.RequestID != claim.Request.RequestID {
		t.Fatalf("idempotent ClaimNext() = %#v, %v", duplicateClaim, err)
	}
	if _, err := service.ClaimNext(storeTestTime.Add(time.Minute), "lease-2", "worker-2", time.Minute, "claim-2", "worker-2", "claim", QueueOptions{MaxGlobalConcurrency: 1}); !errors.Is(err, ErrQueueCapacity) {
		t.Fatalf("capacity claim error = %v, want ErrQueueCapacity", err)
	}
	if _, exists, err := service.GetLease("account-a"); err != nil || !exists {
		t.Fatalf("claim lease = exists:%t err:%v", exists, err)
	}
	if _, err := service.ClaimNext(storeTestTime.Add(2*time.Minute), "lease-1", "worker-1", time.Minute, "claim-1", "worker-1", "claim", QueueOptions{MaxGlobalConcurrency: 1}); !errors.Is(err, account.ErrLeaseExpired) {
		t.Fatalf("expired duplicate claim error = %v, want ErrLeaseExpired", err)
	}
}

func TestStoreRetryKeepsQueueIndexAtCreatedAt(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.SubmitRequest(request, account.Event{
		EventID:    "submit-1",
		AccountID:  request.AccountID,
		RequestID:  request.RequestID,
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "submit",
		Actor:      "test",
		OccurredAt: storeTestTime,
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimNext(storeTestTime.Add(time.Second), "lease-1", "worker-1", time.Minute, "claim-1", "worker-1", "claim", QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	started := account.Event{
		EventID:          "start-1",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.Starting,
		ExpectedRevision: claim.Transition.State.Revision,
		To:               account.LoggingIn,
		Reason:           "start",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(2 * time.Second),
	}
	if _, err := service.ApplyEvent(started); err != nil {
		t.Fatal(err)
	}
	failure := account.Event{
		EventID:          "failure-1",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.LoggingIn,
		ExpectedRevision: started.ExpectedRevision + 1,
		To:               account.LoginFailed,
		Reason:           "transient failure",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(3 * time.Second),
	}
	retry := account.Event{
		EventID:          "retry-1",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.LoginFailed,
		ExpectedRevision: failure.ExpectedRevision + 1,
		To:               account.Queued,
		Reason:           "retry",
		Actor:            "test",
		OccurredAt:       failure.OccurredAt,
	}
	nextAttemptAt := storeTestTime.Add(time.Minute)
	if _, err := service.RecordFailure(failure, account.TransientFailure, nextAttemptAt, &retry); err != nil {
		t.Fatal(err)
	}
	repeated, err := service.RecordFailure(failure, account.TransientFailure, nextAttemptAt, &retry)
	if err != nil || repeated.Retried == nil || !repeated.Retried.Idempotent {
		t.Fatalf("idempotent failure retry = %#v, %v", repeated, err)
	}
	queued, err := service.GetRequest(request.RequestID)
	if err != nil || queued.State != account.Queued || !queued.NotBefore.Equal(nextAttemptAt) {
		t.Fatalf("retry request = %#v, %v", queued, err)
	}
	err = service.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.QueueBucket))
		var keys [][]byte
		var values []string
		if err := bucket.ForEach(func(key, value []byte) error {
			if value != nil {
				keys = append(keys, append([]byte(nil), key...))
				values = append(values, string(value))
			}
			return nil
		}); err != nil {
			return err
		}
		if len(keys) != 1 || string(keys[0]) != string(queueKey(request.CreatedAt, request.RequestID)) || len(values) != 1 || values[0] != request.RequestID {
			return fmt.Errorf("queue entries = %#v/%#v", keys, values)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreOwnedCancellationDoesNotDeleteReplacementLease(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	if _, _, err := service.SubmitRequest(request, account.Event{
		EventID:    "submit-1",
		AccountID:  request.AccountID,
		RequestID:  request.RequestID,
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "submit",
		Actor:      "test",
		OccurredAt: storeTestTime,
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimNext(storeTestTime, "lease-1", "worker-1", time.Minute, "claim-1", "worker-1", "claim", QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcquireLease(request.AccountID, storeTestTime.Add(time.Minute), "lease-2", "worker-2", time.Minute); err != nil {
		t.Fatal(err)
	}
	state, err := service.GetAccount(request.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	cancel := account.Event{
		EventID:          "recovery-1",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.Starting,
		ExpectedRevision: state.Revision,
		To:               account.Cancelled,
		Reason:           "expired starting lease",
		Actor:            "recovery",
		OccurredAt:       storeTestTime.Add(time.Minute),
	}
	if _, err := service.CancelRequestOwned(cancel, claim.Lease, true); !errors.Is(err, account.ErrLeaseNotOwned) {
		t.Fatalf("stale owned cancellation error = %v, want ErrLeaseNotOwned", err)
	}
	if lease, exists, err := service.GetLease(request.AccountID); err != nil || !exists || lease.LeaseID != "lease-2" {
		t.Fatalf("replacement lease after stale cancellation = %#v, %t, %v", lease, exists, err)
	}
	if current, err := service.GetAccount(request.AccountID); err != nil || current.Status != account.Starting {
		t.Fatalf("state after stale cancellation = %#v, %v", current, err)
	}
}

func TestStoreIdempotentCompletionDoesNotReleaseNewLease(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	request := createRequest(t, service, "request-1", "account-1", "idem-1", storeTestTime)
	submit := account.Event{
		EventID:    "submit-1",
		AccountID:  request.AccountID,
		RequestID:  request.RequestID,
		From:       account.NoRequest,
		To:         account.Queued,
		Reason:     "submit",
		Actor:      "test",
		OccurredAt: storeTestTime,
	}
	if _, _, err := service.SubmitRequest(request, submit); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimNext(storeTestTime.Add(time.Second), "lease-1", "worker-1", time.Minute, "claim-1", "worker-1", "claim", QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	started := account.Event{
		EventID:          "start-1",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.Starting,
		ExpectedRevision: claim.Transition.State.Revision,
		To:               account.LoggingIn,
		Reason:           "start",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(2 * time.Second),
	}
	if _, err := service.ApplyEvent(started); err != nil {
		t.Fatal(err)
	}
	success := account.Event{
		EventID:          "success-1",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.LoggingIn,
		ExpectedRevision: started.ExpectedRevision + 1,
		To:               account.LoginSucceeded,
		Reason:           "success",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(3 * time.Second),
	}
	if _, err := service.CompleteRequest(success); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, service, "expire-1", request.AccountID, request.RequestID, account.LoginSucceeded, account.Expired, storeTestTime.Add(4*time.Second))
	applyEvent(t, service, "retry-1", request.AccountID, request.RequestID, account.Expired, account.Queued, storeTestTime.Add(5*time.Second))
	claim, err = service.ClaimNext(storeTestTime.Add(6*time.Second), "lease-2", "worker-2", time.Minute, "claim-2", "worker-2", "claim", QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	started2 := account.Event{
		EventID:          "start-2",
		AccountID:        request.AccountID,
		RequestID:        request.RequestID,
		From:             account.Starting,
		ExpectedRevision: claim.Transition.State.Revision,
		To:               account.LoggingIn,
		Reason:           "start",
		Actor:            "test",
		OccurredAt:       storeTestTime.Add(7 * time.Second),
	}
	if _, err := service.ApplyEvent(started2); err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.CompleteRequest(success)
	if err != nil || !duplicate.Idempotent {
		t.Fatalf("duplicate completion = %#v, %v", duplicate, err)
	}
	if lease, exists, err := service.GetLease(request.AccountID); err != nil || !exists || lease.LeaseID != "lease-2" {
		t.Fatalf("lease after duplicate completion = %#v, %t, %v", lease, exists, err)
	}
}
