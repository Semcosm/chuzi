package diagnostics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/observability"
)

func TestSubmitRedactsUserDataAndQueuesOffline(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	service, err := New(Config{
		Enabled:  true,
		QueueDir: filepath.Join(root, "diagnostics"),
		Version:  "test",
		Clock:    func() time.Time { return now },
		Events: func() []observability.Event {
			return []observability.Event{{RequestID: "account-secret", Resource: "C:\\Users\\Chen", ErrorClass: "rdp_certificate"}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Submit(context.Background(), ReportInput{Severity: SeverityError, Category: "rdp", Summary: "RDP failed: password=secret"})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "queued" || status.ID == "" {
		t.Fatalf("status = %#v", status)
	}
	files, err := os.ReadDir(filepath.Join(root, "diagnostics"))
	if err != nil || len(files) != 1 {
		t.Fatalf("queue files = %v, err=%v", files, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "diagnostics", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"secret", "account-secret", "C:\\Users\\Chen", "password="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("report contains forbidden value %q: %s", forbidden, text)
		}
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	report := envelope["report"].(map[string]any)
	if report["category"] != "rdp" || report["severity"] != SeverityError {
		t.Fatalf("report metadata = %#v", report)
	}
	info, err := files[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("queue file mode = %o", info.Mode().Perm())
	}
}

func TestSubmitUploadsAndRemovesQueueItem(t *testing.T) {
	root := t.TempDir()
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", request.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode = %v", err)
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	service, err := New(Config{Enabled: true, Endpoint: server.URL, QueueDir: filepath.Join(root, "diagnostics"), Clock: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Submit(context.Background(), ReportInput{Severity: SeverityWarning, Category: "core", Summary: "Core unavailable"})
	if err != nil || status.State != "submitted" {
		t.Fatalf("status = %#v, err=%v", status, err)
	}
	files, err := os.ReadDir(filepath.Join(root, "diagnostics"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("queue not emptied: %v", files)
	}
	if received["summary"] != "Core unavailable" {
		t.Fatalf("received payload = %#v", received)
	}
}

func TestFlushRetriesOnlyAfterBackoff(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	service, err := New(Config{Enabled: true, Endpoint: server.URL, QueueDir: filepath.Join(root, "diagnostics"), RetryBase: time.Second, RetryMax: time.Minute, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Submit(context.Background(), ReportInput{Severity: SeverityWarning, Category: "network", Summary: "network warning"})
	if err != nil || status.Attempts != 1 {
		t.Fatalf("status = %#v, err=%v", status, err)
	}
	if err := service.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := service.Pending()
	if err != nil || len(items) != 1 || items[0].Attempts != 1 {
		t.Fatalf("pending = %#v, err=%v", items, err)
	}
	now = now.Add(2 * time.Second)
	_ = service.Flush(context.Background())
	items, err = service.Pending()
	if err != nil || len(items) != 1 || items[0].Attempts != 2 {
		t.Fatalf("pending after retry = %#v, err=%v", items, err)
	}
}
