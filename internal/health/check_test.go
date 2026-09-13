package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
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

func TestCheckerRunsProbesConcurrentlyAndHonorsCallerCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	checker, err := NewCheckerWithConfig(map[string]Probe{
		"first":  func(ctx context.Context) error { started <- struct{}{}; <-release; return ctx.Err() },
		"second": func(ctx context.Context) error { started <- struct{}{}; <-release; return ctx.Err() },
	}, Config{ProbeTimeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan Snapshot, 1)
	go func() { result <- checker.Check(ctx) }()
	for index := 0; index < 2; index++ {
		<-started
	}
	cancel()
	close(release)
	snapshot := <-result
	if snapshot.Status != Unhealthy || snapshot.Checks["first"].Status != Unhealthy || snapshot.Checks["second"].Status != Unhealthy {
		t.Fatalf("cancelled probe snapshot = %#v", snapshot)
	}
}

func TestCheckerSafetyDeadlineCancelsPendingProbes(t *testing.T) {
	started := make(chan struct{})
	observedCancellation := make(chan struct{})
	checker, err := NewCheckerWithConfig(map[string]Probe{
		"slow": func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(observedCancellation)
			return ctx.Err()
		},
	}, Config{ProbeTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Snapshot, 1)
	go func() { result <- checker.Check(context.Background()) }()
	<-started
	snapshot := <-result
	<-observedCancellation
	if snapshot.Status != Unhealthy || snapshot.Checks["slow"].Status != Unhealthy {
		t.Fatalf("deadline snapshot = %#v", snapshot)
	}
}
