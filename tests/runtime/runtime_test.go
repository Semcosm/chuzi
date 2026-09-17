package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/health"
	"github.com/Semcosm/chuzi/internal/matrix"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/queue"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var runtimeTime = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// testHomeserver is a small Client-Server API implementation. Unlike a
// handler-only client mock, it keeps sync batches and Matrix transaction IDs
// in durable-looking server state so the complete gateway/outbox protocol is
// exercised over HTTP.
type testHomeserver struct {
	mu             sync.Mutex
	token          string
	server         *httptest.Server
	syncEvents     []matrix.SyncEvent
	nextBatch      int
	sends          []matrixSend
	failNotifyOnce bool
}

type matrixSend struct {
	RoomID  string
	EventID string
	Body    string
}

func newTestHomeserver(t *testing.T, token string) *testHomeserver {
	t.Helper()
	h := &testHomeserver{token: token}
	h.server = httptest.NewServer(http.HandlerFunc(h.serveHTTP))
	t.Cleanup(h.server.Close)
	return h
}

func (h *testHomeserver) URL() string { return h.server.URL }

func (h *testHomeserver) enqueue(event matrix.SyncEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.syncEvents = append(h.syncEvents, event)
}

func (h *testHomeserver) sent() []matrixSend {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := append([]matrixSend(nil), h.sends...)
	return result
}

func (h *testHomeserver) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer "+h.token {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/_matrix/client/v3/account/whoami":
		writeJSON(writer, map[string]string{"user_id": "@bot:example.org"})
	case request.Method == http.MethodGet && request.URL.Path == "/_matrix/client/v3/sync":
		h.handleSync(writer, request)
	case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/send/m.room.message/"):
		h.handleSend(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (h *testHomeserver) handleSync(writer http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.syncEvents) == 0 {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	events := append([]matrix.SyncEvent(nil), h.syncEvents...)
	h.syncEvents = nil
	h.nextBatch++
	writeJSON(writer, map[string]any{
		"next_batch": fmt.Sprintf("batch-%d", h.nextBatch),
		"rooms": map[string]any{"join": map[string]any{
			"!ops:example.org": map[string]any{"timeline": map[string]any{"events": events}},
		}},
	})
}

func (h *testHomeserver) handleSend(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var payload struct {
		Body string `json:"body"`
	}
	if json.Unmarshal(body, &payload) != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/_matrix/client/v3/rooms/"), "/send/m.room.message/")
	if len(parts) != 2 {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	roomID, _ := url.PathUnescape(parts[0])
	eventID, _ := url.PathUnescape(parts[1])
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failNotifyOnce && strings.HasPrefix(payload.Body, "[chuzi]") {
		h.failNotifyOnce = false
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	h.sends = append(h.sends, matrixSend{RoomID: roomID, EventID: eventID, Body: payload.Body})
	writeJSON(writer, map[string]string{"event_id": eventID})
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func newRuntimeStore(t *testing.T) (*store.Store, config.Config) {
	t.Helper()
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, cfg
}

func newRequestService(t *testing.T, database *store.Store, now *time.Time) *requestservice.Service {
	t.Helper()
	sequence := 0
	service, err := requestservice.New(database, func() time.Time { return *now }, func(kind string) string {
		sequence++
		return fmt.Sprintf("%s-%d", kind, sequence)
	}, "runtime-test")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestProductionMatrixSyncSendAndOutboxRecoveryAcrossRestart(t *testing.T) {
	database, cfg := newRuntimeStore(t)
	if _, err := database.CreateAccount("account-matrix-secret"); err != nil {
		t.Fatal(err)
	}
	now := runtimeTime
	requests := newRequestService(t, database, &now)
	adapter, err := matrix.NewAdapter(requests, matrix.Policy{Rooms: map[string]map[string]matrix.Role{
		"!ops:example.org": {"@alice:example.org": matrix.RoleUser},
	}}, matrix.Config{UserID: "@bot:example.org", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	homeserver := newTestHomeserver(t, "test-matrix-token")
	homeserver.failNotifyOnce = true
	client, err := matrix.NewHTTPClient(matrix.HTTPClientConfig{HomeserverURL: homeserver.URL(), AccessToken: "test-matrix-token", HTTPClient: &http.Client{Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("whoami health check failed: %v", err)
	}
	homeserver.enqueue(matrix.SyncEvent{Type: "m.room.message", EventID: "$command-1", Sender: "@alice:example.org", Content: struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	}{MsgType: "m.text", Body: "!ugs request account-matrix-secret"}})
	gateway, err := matrix.NewGateway(matrix.GatewayConfig{Client: client, Adapter: adapter, SyncTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- gateway.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for len(homeserver.sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("sync gateway error = %v", err)
	}
	sent := homeserver.sent()
	if len(sent) != 1 || !strings.Contains(sent[0].Body, "status=QUEUED") || strings.Contains(sent[0].Body, "account-matrix-secret") {
		t.Fatalf("gateway reply = %#v", sent)
	}

	notifier, err := matrix.NewNotifier(database, client, matrix.NotifierConfig{
		Owner: "runtime-notifier", ClaimTTL: time.Minute, RetryBase: time.Second, RetryMax: time.Second,
		BatchSize: 10, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	now = runtimeTime.Add(time.Second)
	first, err := notifier.Flush(context.Background())
	if err != nil || first.Claimed != 1 || first.Retried != 1 {
		t.Fatalf("initial failed outbox flush = %#v, %v", first, err)
	}
	queued, err := database.ListNotifications()
	if err != nil || len(queued) != 1 {
		t.Fatalf("outbox after gateway and failed notifier = %#v, %v", queued, err)
	}
	var notificationEventID string
	for _, notification := range queued {
		if notification.State == account.Queued {
			notificationEventID = notification.EventID
		}
	}
	if notificationEventID == "" {
		t.Fatalf("could not identify queued notification: %#v", queued)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	now = runtimeTime.Add(2 * time.Second)
	restartedNotifier, err := matrix.NewNotifier(restarted, client, matrix.NotifierConfig{
		Owner: "runtime-notifier-restarted", ClaimTTL: time.Minute, RetryBase: time.Second, RetryMax: time.Second,
		BatchSize: 10, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := restartedNotifier.Flush(context.Background())
	if err != nil || second.Delivered != 1 {
		t.Fatalf("restarted outbox flush = %#v, %v", second, err)
	}
	sent = homeserver.sent()
	var notificationSends []matrixSend
	for _, item := range sent {
		if strings.HasPrefix(item.Body, "[chuzi]") {
			notificationSends = append(notificationSends, item)
		}
	}
	if len(notificationSends) != 1 || notificationSends[0].EventID != notificationEventID {
		t.Fatalf("notification delivery IDs = %#v, want %q", notificationSends, notificationEventID)
	}
	if strings.Contains(notificationSends[0].Body, "account-matrix-secret") || strings.Contains(notificationSends[0].Body, "!ops:example.org") {
		t.Fatalf("notification leaked identifiers: %q", notificationSends[0].Body)
	}
}

func TestProductionCredentialLifecycleAndRedactedObservability(t *testing.T) {
	database, _ := newRuntimeStore(t)
	if _, err := database.CreateAccount("account-credential-secret"); err != nil {
		t.Fatal(err)
	}
	oldKey, err := credential.NewKey("key-old", bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := credential.NewStaticKeyring(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	service, err := credential.New(database, keys)
	if err != nil {
		t.Fatal(err)
	}
	secret := "credential-test-secret"
	t.Setenv("CHUZI_RUNTIME_CREDENTIAL", secret)
	source, err := credential.NewEnvSource("CHUZI_RUNTIME_CREDENTIAL")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Inject(context.Background(), "account-credential-secret", "operator", runtimeTime, source); err != nil {
		t.Fatal(err)
	}
	newKey, err := credential.NewKey("key-current", bytes.Repeat([]byte{0x22}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.SetCurrent(newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(context.Background(), "account-credential-secret", "operator", runtimeTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var recovered string
	if err := service.Use(context.Background(), "account-credential-secret", "worker", runtimeTime.Add(2*time.Minute), func(value []byte) error {
		recovered = string(value)
		return nil
	}); err != nil || recovered != secret {
		t.Fatalf("rotated credential use = %q, %v", recovered, err)
	}
	if err := service.Revoke(context.Background(), "account-credential-secret", "operator", runtimeTime.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	record, found, err := database.GetCredential("account-credential-secret")
	if err != nil || !found || record.RevokedAt == nil || len(record.Ciphertext) != 0 || len(record.Nonce) != 0 {
		t.Fatalf("revoked credential record = %#v/%t: %v", record, found, err)
	}

	var log bytes.Buffer
	logger, err := observability.NewJSONLogger(observability.LoggerConfig{Writer: &log})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	logger.Record(observability.Event{Component: "credential", Operation: "rotate", Outcome: "accepted", RequestID: secret, Resource: "!private-room:example.org"})
	if strings.Contains(log.String(), secret) || strings.Contains(log.String(), "private-room:example.org") {
		t.Fatalf("structured logs leaked secret material: %s", log.String())
	}
	audits, err := database.ListAuditEntries(store.AuditQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(audits)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "account-credential-secret") {
		t.Fatalf("audit output leaked credential data: %s", encoded)
	}
}

type runtimeRunner struct {
	result queue.Result
	err    error
	wait   bool
}

func (r runtimeRunner) Run(ctx context.Context, _ queue.Work) (queue.Result, error) {
	if r.wait {
		<-ctx.Done()
		return queue.Result{}, ctx.Err()
	}
	return r.result, r.err
}

func TestProductionWorkerTimeoutCrashLeaseRecoveryAndRestart(t *testing.T) {
	database, cfg := newRuntimeStore(t)
	if _, err := database.CreateAccount("account-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAccount("account-crash"); err != nil {
		t.Fatal(err)
	}
	now := runtimeTime
	requests := newRequestService(t, database, &now)
	if _, _, err := requests.Submit(requestservice.SubmitInput{RequestID: "request-worker", AccountID: "account-worker", IdempotencyKey: "idem-worker"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := requests.Submit(requestservice.SubmitInput{RequestID: "request-crash", AccountID: "account-crash", IdempotencyKey: "idem-crash"}); err != nil {
		t.Fatal(err)
	}
	ids := 0
	newID := func(kind string) string {
		ids++
		return fmt.Sprintf("%s-%d", kind, ids)
	}
	crashScheduler, err := queue.New(database, runtimeRunner{err: errors.New("worker process crashed with secret payload")}, queue.Config{
		Owner: "runtime-worker", LeaseTTL: time.Minute, RunTimeout: 5 * time.Millisecond, MaxGlobalConcurrency: 1,
		RetryPolicy: account.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second},
		Clock:       func() time.Time { return now }, NewID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}
	crashOutcome, err := crashScheduler.RunOnce(context.Background())
	if err != nil || !crashOutcome.Retried || crashOutcome.Request.RequestID != "request-crash" || crashOutcome.Request.State != account.Queued {
		t.Fatalf("worker crash outcome = %#v, %v", crashOutcome, err)
	}
	failedScheduler, err := queue.New(database, runtimeRunner{wait: true}, queue.Config{
		Owner: "runtime-worker", LeaseTTL: time.Minute, RunTimeout: 5 * time.Millisecond, MaxGlobalConcurrency: 1,
		RetryPolicy: account.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second},
		Clock:       func() time.Time { return now }, NewID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := failedScheduler.RunOnce(context.Background())
	if err != nil || !outcome.Retried || outcome.Request.State != account.Queued {
		t.Fatalf("worker timeout outcome = %#v, %v", outcome, err)
	}
	if _, exists, err := database.GetLease("account-worker"); err != nil || exists {
		t.Fatalf("timeout lease = exists:%t err:%v", exists, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	now = runtimeTime.Add(2 * time.Second)
	ids = 100
	successScheduler, err := queue.New(restarted, runtimeRunner{result: queue.Result{Succeeded: true}}, queue.Config{
		Owner: "runtime-worker-restarted", LeaseTTL: time.Minute, RunTimeout: time.Second, MaxGlobalConcurrency: 1,
		RetryPolicy: account.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second},
		Clock:       func() time.Time { return now }, NewID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = successScheduler.RunOnce(context.Background())
	if err != nil || !outcome.Succeeded || outcome.Request.State != account.LoginSucceeded {
		t.Fatalf("restart recovery outcome = %#v, %v", outcome, err)
	}
	if err := restarted.ValidateDatabase(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionExpiredLeaseIsRecoveredAfterProcessRestart(t *testing.T) {
	database, cfg := newRuntimeStore(t)
	if _, err := database.CreateAccount("account-orphan"); err != nil {
		t.Fatal(err)
	}
	request, err := store.NewRequest("request-orphan", "account-orphan", "idem-orphan", runtimeTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateRequest(request); err != nil {
		t.Fatal(err)
	}
	queued := account.Event{EventID: "event-orphan-queued", AccountID: "account-orphan", RequestID: "request-orphan", From: account.NoRequest, To: account.Queued, Reason: "runtime test", Actor: "runtime-test", OccurredAt: runtimeTime}
	if _, err := database.ApplyEvent(queued); err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(runtimeTime, "lease-orphan", "crashed-service", time.Second, "event-orphan-start", "crashed-service", "queue claim", store.QueueOptions{MaxGlobalConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	state, err := database.GetAccount("account-orphan")
	if err != nil {
		t.Fatal(err)
	}
	logging := account.Event{EventID: "event-orphan-logging", AccountID: "account-orphan", RequestID: "request-orphan", From: account.Starting, ExpectedRevision: state.Revision, To: account.LoggingIn, Reason: "runtime test", Actor: "crashed-service", OccurredAt: runtimeTime}
	if _, err := database.ApplyEvent(logging); err != nil {
		t.Fatal(err)
	}
	if claim.Lease.LeaseID != "lease-orphan" {
		t.Fatal("test did not create the orphan lease")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	now := runtimeTime.Add(2 * time.Second)
	ids := 0
	scheduler, err := queue.New(restarted, runtimeRunner{result: queue.Result{Succeeded: true}}, queue.Config{
		Owner: "restarted-service", LeaseTTL: time.Minute, RunTimeout: time.Second, MaxGlobalConcurrency: 1,
		RetryPolicy: account.RetryPolicy{MaxAttempts: 2, BaseDelay: 0, MaxDelay: 0},
		Clock:       func() time.Time { return now }, NewID: func(kind string) string {
			ids++
			return fmt.Sprintf("recovered-%s-%d", kind, ids)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := scheduler.RunOnce(context.Background())
	if err != nil || !outcome.Succeeded || outcome.Request.State != account.LoginSucceeded {
		t.Fatalf("expired lease recovery outcome = %#v, %v", outcome, err)
	}
	if _, exists, err := restarted.GetLease("account-orphan"); err != nil || exists {
		t.Fatalf("recovered lease = exists:%t err:%v", exists, err)
	}
}

func TestProductionBackupRestoreRejectsCorruptionAndPreservesActiveData(t *testing.T) {
	database, cfg := newRuntimeStore(t)
	if _, err := database.CreateAccount("account-backup"); err != nil {
		t.Fatal(err)
	}
	backupPath, err := database.Backup(runtimeTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBackup(backupPath); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(cfg.BackupDir(), "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("not-bbolt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBackup(corruptPath); !errors.Is(err, store.ErrInvalidRestore) {
		t.Fatalf("corrupt backup error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(cfg, corruptPath); !errors.Is(err, store.ErrInvalidRestore) {
		t.Fatalf("corrupt restore error = %v", err)
	}
	reopened, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.GetAccount("account-backup"); err != nil {
		t.Fatalf("active database was changed by rejected restore: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(cfg, backupPath); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.GetAccount("account-backup"); err != nil {
		t.Fatalf("restored account missing: %v", err)
	}
}

func TestProductionHealthMetricsAndAuditAreSafeToExpose(t *testing.T) {
	metrics := observability.NewMetrics()
	checker, err := health.NewChecker(map[string]health.Probe{
		"service": health.StaticProbe(nil),
		"matrix":  health.StaticProbe(errors.New("access token secret")),
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics.Record(observability.Event{Component: "matrix", Operation: "send", Outcome: "failed", RequestID: "account-secret", Resource: "!room-secret:example.org", ErrorClass: "send_failed"})
	exposition := metrics.Prometheus()
	if strings.Contains(exposition, "account-secret") || strings.Contains(exposition, "room-secret") || !strings.Contains(exposition, "send_failed") {
		t.Fatalf("metrics exposition = %s", exposition)
	}
	snapshot := checker.Check(context.Background())
	if snapshot.Status != health.Unhealthy || snapshot.Checks["service"].Status != health.Healthy || snapshot.Checks["matrix"].Status != health.Unhealthy {
		t.Fatalf("health snapshot = %#v", snapshot)
	}
	record := httptest.NewRecorder()
	checker.Handler().ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if record.Code != http.StatusServiceUnavailable || strings.Contains(record.Body.String(), "access token secret") {
		t.Fatalf("health response = %d %q", record.Code, record.Body.String())
	}
}
