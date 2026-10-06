package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/diagnostics"
	"github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/session"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

type viewServiceRequests struct{}

func (viewServiceRequests) Submit(request.SubmitInput) (store.Request, bool, error) {
	return store.Request{}, false, errors.New("not used")
}
func (viewServiceRequests) Status(string) (store.Request, error) {
	return store.Request{}, errors.New("not used")
}
func (viewServiceRequests) Cancel(string, string, string) (store.Request, error) {
	return store.Request{}, errors.New("not used")
}

type rateLimitedServiceRequests struct{}

func (rateLimitedServiceRequests) Submit(request.SubmitInput) (store.Request, bool, error) {
	return store.Request{}, false, request.ErrRateLimited
}
func (rateLimitedServiceRequests) Status(string) (store.Request, error) {
	return store.Request{}, errors.New("not used")
}
func (rateLimitedServiceRequests) Cancel(string, string, string) (store.Request, error) {
	return store.Request{}, errors.New("not used")
}

type viewServiceStore struct{}

func (viewServiceStore) GetAccount(string) (account.Snapshot, error) { return account.Snapshot{}, nil }
func (viewServiceStore) ListRequests() ([]store.Request, error)      { return nil, nil }
func (viewServiceStore) ListAuditEntries(store.AuditQuery) ([]store.AuditEntry, error) {
	return nil, nil
}
func (viewServiceStore) QueryNotifications(store.NotificationQuery) ([]store.Notification, error) {
	return nil, nil
}

type listServiceStore struct {
	requests []store.Request
	err      error
}

func (s listServiceStore) GetAccount(string) (account.Snapshot, error) {
	return account.Snapshot{}, nil
}
func (s listServiceStore) ListRequests() ([]store.Request, error) { return s.requests, s.err }
func (listServiceStore) ListAuditEntries(store.AuditQuery) ([]store.AuditEntry, error) {
	return nil, nil
}
func (listServiceStore) QueryNotifications(store.NotificationQuery) ([]store.Notification, error) {
	return nil, nil
}

type viewServicePort struct{}

func (viewServicePort) Snapshot(context.Context, string, int, int) (browser.ViewSnapshot, error) {
	return browser.ViewSnapshot{ContentType: "image/jpeg", Width: 320, Height: 180, Data: []byte("jpeg")}, nil
}

type failedViewServicePort struct{}

func (failedViewServicePort) Snapshot(context.Context, string, int, int) (browser.ViewSnapshot, error) {
	return browser.ViewSnapshot{}, errors.New("adapter details must stay behind the Core boundary")
}

type rdpRequestPort struct{ request store.Request }

func (rdpRequestPort) Submit(request.SubmitInput) (store.Request, bool, error) {
	return store.Request{}, false, nil
}
func (p rdpRequestPort) Status(string) (store.Request, error) { return p.request, nil }
func (rdpRequestPort) Cancel(string, string, string) (store.Request, error) {
	return store.Request{}, nil
}

type rdpCapabilityPort struct{}

func (rdpCapabilityPort) Issue(context.Context, string, string, string) (coreapi.RDPCapability, error) {
	return coreapi.RDPCapability{ID: "rdp_abc", Token: "opaque-token", RequestID: "request-1", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

type testDiagnosticsPort struct{}

func (testDiagnosticsPort) Submit(context.Context, diagnostics.ReportInput) (diagnostics.Status, error) {
	return diagnostics.Status{ID: "diag-1", State: "queued", CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()}, nil
}

type testJobPoolPort struct{}

func (testJobPoolPort) SlotPoolStatus(string, time.Time) (slot.StatusCounts, error) {
	return slot.StatusCounts{PoolID: "pool-test", Desired: 5, Ready: 3, Leased: 1, Quarantined: 1, Draining: 1}, nil
}

type sessionPortFixture struct {
	record  store.SessionRecord
	started int
	stopped int
}

func (p *sessionPortFixture) Start(context.Context, session.StartInput) (store.SessionRecord, error) {
	p.started++
	return p.record, nil
}
func (p *sessionPortFixture) Get(string) (store.SessionRecord, error) { return p.record, nil }
func (p *sessionPortFixture) List() ([]store.SessionRecord, error) {
	return []store.SessionRecord{p.record}, nil
}
func (p *sessionPortFixture) Stop(context.Context, string, string) (store.SessionRecord, error) {
	p.stopped++
	p.record.Phase = string(session.Stopped)
	return p.record, nil
}

func TestSessionAPIProjectsLifecycleWithoutRuntimeSecrets(t *testing.T) {
	at := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	fixture := &sessionPortFixture{record: store.SessionRecord{
		SessionID: "session-1", RequestID: "request-1", AccountID: "account-private", PoolID: "pool-1",
		EnvironmentID: "env-1", EnvironmentVersion: "1.0.0", AdapterID: "adapter-1", AdapterVersion: "1.0.0",
		Phase: string(session.RDPAvailable), SlotID: "slot-secret", SlotLeaseID: "lease-secret", AccountLeaseID: "account-lease-secret",
		EnvironmentGeneration: 7, AgentReady: true, WorkerReady: true, AdapterReady: true, CreatedAt: at, UpdatedAt: at,
	}}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Sessions: fixture, MaxConcurrency: 2, Clock: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.StartSession(context.Background(), coreapi.SessionStartRequest{
		SessionID: "session-1", RequestID: "request-1", AccountID: "account-private", PoolID: "pool-1",
		EnvironmentID: "env-1", EnvironmentVersion: "1.0.0", AdapterID: "adapter-1", AdapterVersion: "1.0.0",
	})
	if err != nil || item.SessionID != "session-1" || item.Account == "account-private" || !item.RDPAvailable || item.SlotState != "leased" {
		t.Fatalf("start projection = %#v, err=%v", item, err)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"slot-secret", "lease-secret", "account-lease-secret", "account-private"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("session projection leaked %q: %s", secret, raw)
		}
	}
	items, err := service.ListSessions(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %#v, err=%v", items, err)
	}
	stopped, err := service.StopSession(context.Background(), coreapi.SessionStopRequest{SessionID: "session-1", Reason: "operator request"})
	if err != nil || stopped.Phase != string(session.Stopped) || fixture.stopped != 1 {
		t.Fatalf("stop = %#v, err=%v, calls=%d", stopped, err, fixture.stopped)
	}
}

func TestStartSessionRejectsInvalidInputsBeforeSubmittingRequest(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Sessions: &sessionPortFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.StartSession(context.Background(), coreapi.SessionStartRequest{AccountID: "account-1", IdempotencyKey: "idem-1", PoolID: "pool/escape", EnvironmentID: "env-1", EnvironmentVersion: "1.0.0", AdapterID: "adapter-1", AdapterVersion: "1.0.0"})
	if coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("invalid input error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestGetJobPoolStatusProjectsCapacityWithoutSensitiveFields(t *testing.T) {
	service, err := New(Dependencies{
		Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPools: testJobPoolPort{},
		JobPoolID: "pool-test", MaxConcurrency: 2, Clock: func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.GetJobPoolStatus(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if status.PoolID != "pool-test" || status.Desired != 5 || status.Ready != 3 || status.Leased != 1 || status.Quarantined != 1 || status.Draining != 1 || status.EffectiveCapacity != 2 {
		t.Fatalf("job pool status = %#v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password", "username", "sid", "Profile", "rdp", "cmd.exe"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(secret)) {
			t.Fatalf("job pool status leaked %q: %s", secret, raw)
		}
	}
}

func TestSubmitDiagnosticReportUsesOptionalPort(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Diagnostics: testDiagnosticsPort{}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.SubmitDiagnosticReport(context.Background(), coreapi.DiagnosticReport{Severity: "error", Category: "core", Summary: "Core unavailable"})
	if err != nil || status.ID != "diag-1" || status.State != "queued" {
		t.Fatalf("status = %#v, err=%v", status, err)
	}
}

func TestSubmitRequestMapsRateLimitToStableCoreCode(t *testing.T) {
	service, err := New(Dependencies{Requests: rateLimitedServiceRequests{}, Store: viewServiceStore{}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.SubmitRequest(context.Background(), coreapi.SubmitRequest{
		RequestID: "request-1", AccountID: "account-1", IdempotencyKey: "idempotency-1",
	})
	if got := coreapi.CodeOf(err); got != coreapi.CodeRateLimited {
		t.Fatalf("error code = %q, want %q", got, coreapi.CodeRateLimited)
	}
	if err.Error() != "chuzi core: rate_limited" {
		t.Fatalf("error text = %q", err)
	}
}

func TestGetBrowserViewReturnsEphemeralBase64Frame(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Views: viewServicePort{}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.GetBrowserView(context.Background(), coreapi.BrowserViewRequest{RequestID: "request-1", Width: 320, Height: 180})
	if err != nil {
		t.Fatal(err)
	}
	if view.RequestID != "request-1" || view.Width != 320 || view.Height != 180 || view.ContentType != "image/jpeg" {
		t.Fatalf("unexpected view metadata: %#v", view)
	}
	decoded, err := base64.StdEncoding.DecodeString(view.Data)
	if err != nil || string(decoded) != "jpeg" {
		t.Fatalf("view data = %q, err=%v", decoded, err)
	}
	if view.CapturedAt.Before(time.Unix(0, 0)) {
		t.Fatalf("captured_at = %v", view.CapturedAt)
	}
}

func TestGetBrowserViewIsUnavailableWithoutActiveSession(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetBrowserView(context.Background(), coreapi.BrowserViewRequest{RequestID: "request-1"})
	if got := coreapi.CodeOf(err); got != coreapi.CodeUnavailable {
		t.Fatalf("error code = %q, want %q", got, coreapi.CodeUnavailable)
	}
}

func TestGetBrowserViewMapsAdapterFailureToUnavailable(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Views: failedViewServicePort{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetBrowserView(context.Background(), coreapi.BrowserViewRequest{RequestID: "request-1"})
	if got := coreapi.CodeOf(err); got != coreapi.CodeUnavailable {
		t.Fatalf("error code = %q, want %q", got, coreapi.CodeUnavailable)
	}
}

func TestIssueRDPCapabilityRequiresAuthorizedRequestAndReturnsOpaqueProjection(t *testing.T) {
	request := store.Request{RequestID: "request-1", AccountID: "account-secret", State: account.LoginSucceeded}
	service, err := New(Dependencies{Requests: rdpRequestPort{request: request}, Store: viewServiceStore{}, RDP: rdpCapabilityPort{}})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.IssueRDPCapability(context.Background(), coreapi.RDPCapabilityRequest{RequestID: "request-1", Actor: "windows-ui"})
	if err != nil || capability.Token != "opaque-token" || capability.RequestID != "request-1" {
		t.Fatalf("capability = %#v, err=%v", capability, err)
	}
	if strings.Contains(capability.Token, "account-secret") {
		t.Fatal("capability token contains account material")
	}

	request.State = account.Queued
	service, _ = New(Dependencies{Requests: rdpRequestPort{request: request}, Store: viewServiceStore{}, RDP: rdpCapabilityPort{}})
	if _, err := service.IssueRDPCapability(context.Background(), coreapi.RDPCapabilityRequest{RequestID: "request-1", Actor: "windows-ui"}); coreapi.CodeOf(err) != coreapi.CodeForbidden {
		t.Fatalf("queued request error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestListRequestsOrdersPaginatesAndRedacts(t *testing.T) {
	created := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	service, err := New(Dependencies{
		Requests: viewServiceRequests{},
		Store: listServiceStore{requests: []store.Request{
			{RequestID: "request-b", AccountID: "account-private-b", IdempotencyKey: "idem-private-b", NotificationRoomID: "!room-private-b", State: account.Queued, CreatedAt: created, UpdatedAt: created},
			{RequestID: "request-a", AccountID: "account-private-a", IdempotencyKey: "idem-private-a", NotificationRoomID: "!room-private-a", State: account.Starting, CreatedAt: created, UpdatedAt: created},
			{RequestID: "request-c", AccountID: "account-private-c", IdempotencyKey: "idem-private-c", NotificationRoomID: "!room-private-c", State: account.LoginFailed, CreatedAt: created.Add(time.Second), UpdatedAt: created.Add(time.Second)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListRequests(context.Background(), coreapi.RequestQuery{Offset: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].RequestID != "request-b" || items[1].RequestID != "request-c" {
		t.Fatalf("items = %#v", items)
	}
	if !strings.HasPrefix(items[0].Account, "id_") || strings.Contains(items[0].Account, "private") {
		t.Fatalf("account was not redacted: %#v", items[0])
	}
	raw, marshalErr := json.Marshal(items)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, forbidden := range []string{"idem-private", "room-private", "notification_room_id", "idempotency_key"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("sensitive field %q crossed projection: %s", forbidden, raw)
		}
	}
}

func TestListRequestsFiltersStateAndReturnsEmptyList(t *testing.T) {
	created := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	service, err := New(Dependencies{
		Requests: viewServiceRequests{},
		Store:    listServiceStore{requests: []store.Request{{RequestID: "request-a", AccountID: "account-a", State: account.Queued, CreatedAt: created, UpdatedAt: created}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListRequests(context.Background(), coreapi.RequestQuery{State: string(account.LoginFailed), Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("empty list = %#v, want non-nil empty list", items)
	}
}

func TestListRequestsRejectsInvalidQueryAndRedactsReaderError(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: listServiceStore{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []coreapi.RequestQuery{{Limit: maxQueryLimit + 1}, {State: "not-a-state"}} {
		if _, err := service.ListRequests(context.Background(), query); coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
			t.Fatalf("query %#v error = %v, code=%q", query, err, coreapi.CodeOf(err))
		}
	}
	failing, err := New(Dependencies{Requests: viewServiceRequests{}, Store: listServiceStore{err: errors.New("bbolt /secret/path must not leak")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = failing.ListRequests(context.Background(), coreapi.RequestQuery{})
	if coreapi.CodeOf(err) != coreapi.CodeInternal || strings.Contains(err.Error(), "/secret/path") {
		t.Fatalf("reader error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}
