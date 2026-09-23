package core

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/diagnostics"
	"github.com/Semcosm/chuzi/internal/request"
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

type viewServiceStore struct{}

func (viewServiceStore) GetAccount(string) (account.Snapshot, error) { return account.Snapshot{}, nil }
func (viewServiceStore) ListAuditEntries(store.AuditQuery) ([]store.AuditEntry, error) {
	return nil, nil
}
func (viewServiceStore) QueryNotifications(store.NotificationQuery) ([]store.Notification, error) {
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

type testDiagnosticsPort struct{}

func (testDiagnosticsPort) Submit(context.Context, diagnostics.ReportInput) (diagnostics.Status, error) {
	return diagnostics.Status{ID: "diag-1", State: "queued", CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()}, nil
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
