package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestJSONLoggerRedactsFieldsAndWritesStructuredLine(t *testing.T) {
	var output bytes.Buffer
	logger, err := NewJSONLogger(LoggerConfig{Writer: &output, Clock: func() time.Time { return time.Unix(10, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Write(Event{Component: "queue", Operation: "claim", Outcome: "failed", RequestID: "request-1", Resource: "!secret-room:example.org", ErrorClass: "transient", Duration: 2 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["request_id"] == "request-1" || record["resource"] == "!secret-room:example.org" || record["duration_ms"] != float64(2) {
		t.Fatalf("record = %#v", record)
	}
	if strings.Contains(output.String(), "secret-room") {
		t.Fatalf("log contains raw room: %s", output.String())
	}
	output.Reset()
	if err := logger.Write(Event{RequestID: "id_000000000000"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "id_000000000000") {
		t.Fatalf("log accepted an unverified redaction label: %s", output.String())
	}
}

func TestJSONLoggerRotatesBoundedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "service.log")
	logger, err := NewJSONLogger(LoggerConfig{Path: path, MaxBytes: 120, MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		logger.Record(Event{Component: "service", Operation: "tick", Outcome: "ok", RequestID: "r"})
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := SortedRotationFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 3 {
		t.Fatalf("rotation files = %#v", files)
	}
	for _, file := range files {
		info, statErr := os.Stat(file)
		if statErr != nil || info.Size() == 0 {
			t.Fatalf("rotation file %s: %v %#v", file, statErr, info)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("rotation file %s permissions = %o, want 600", file, info.Mode().Perm())
		}
	}
}

func TestJSONLoggerRestrictsExistingFileAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "service.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logger, err := NewJSONLogger(LoggerConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("existing log permissions = %o, want 600", info.Mode().Perm())
	}
	target := filepath.Join(root, "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewJSONLogger(LoggerConfig{Path: link}); !errors.Is(err, ErrInvalidLogger) {
		t.Fatalf("symlink logger error = %v, want ErrInvalidLogger", err)
	}
}

func TestMetricsExposeStableLabelsWithoutIdentifiers(t *testing.T) {
	metrics := NewMetrics()
	metrics.Record(Event{Component: "matrix", Operation: "notify", Outcome: "retry", RequestID: "private-request", Resource: "!private-room:example.org", ErrorClass: "send_failed"})
	metrics.Record(Event{Component: "matrix", Operation: "notify", Outcome: "retry", RequestID: "another-request", Resource: "!another-room:example.org", ErrorClass: "send_failed"})
	text := metrics.Prometheus()
	if !strings.Contains(text, "chuzi_events_total") || !strings.Contains(text, `component="matrix"`) {
		t.Fatalf("metrics = %s", text)
	}
	if strings.Contains(text, "private-request") || strings.Contains(text, "private-room") {
		t.Fatalf("metrics leaked identifiers: %s", text)
	}
}

func TestMetricsRejectInvalidHistogramAndLabelDefinitions(t *testing.T) {
	metrics := NewMetrics()
	if err := metrics.Register(MetricDefinition{Name: "invalid_histogram", Kind: Histogram, Buckets: []float64{0.1, math.Inf(1)}}); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("infinite histogram bucket error = %v, want ErrInvalidMetric", err)
	}
	if err := metrics.Register(MetricDefinition{Name: "invalid_counter", Kind: Counter, Buckets: []float64{1}}); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("counter bucket error = %v, want ErrInvalidMetric", err)
	}
	metrics.Observe("valid_histogram", time.Second, Label{Name: "le", Value: "1"})
	if strings.Contains(metrics.Prometheus(), "valid_histogram") {
		t.Fatalf("reserved histogram label was accepted: %s", metrics.Prometheus())
	}
	metrics.Inc("valid_counter", Label{Name: "bad:name", Value: "value"})
	if strings.Contains(metrics.Prometheus(), "bad:name") {
		t.Fatalf("invalid label name was accepted: %s", metrics.Prometheus())
	}
}

func TestMetricsZeroValueIsUsable(t *testing.T) {
	var metrics Metrics
	metrics.Inc("zero_value_events")
	if !strings.Contains(metrics.Prometheus(), "zero_value_events 1") {
		t.Fatalf("zero-value metrics = %s", metrics.Prometheus())
	}
}
