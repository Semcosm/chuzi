package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestCheckerSeparatesDependencyStatusAndRedactsErrors(t *testing.T) {
	checker, err := NewChecker(map[string]Probe{
		"storage": StaticProbe(nil),
		"matrix":  StaticProbe(errors.New("access token secret")),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := checker.Check(context.Background())
	if snapshot.Status != Unhealthy || snapshot.Checks["storage"].Status != Healthy || snapshot.Checks["matrix"].Status != Unhealthy {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	record := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/healthz", nil)
	checker.Handler().ServeHTTP(record, request)
	if record.Code != 503 || string(record.Body.Bytes()) == "" {
		t.Fatalf("health response = %d %q", record.Code, record.Body.String())
	}
	var body Snapshot
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["matrix"].Status != Unhealthy && string(record.Body.Bytes()) != "" {
		t.Fatalf("unexpected health body = %s", record.Body.String())
	}
}
