// Package diagnostics implements explicit, user-consented fault reporting.
package diagnostics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Semcosm/chuzi/internal/observability"
)

var (
	ErrDisabled      = errors.New("diagnostics: disabled")
	ErrInvalidReport = errors.New("diagnostics: invalid report")
	ErrQueue         = errors.New("diagnostics: local queue failure")
	ErrUpload        = errors.New("diagnostics: upload failed")
	ErrInvalidConfig = errors.New("diagnostics: invalid configuration")
)

const (
	SeverityWarning = "warning"
	SeverityError   = "error"
	defaultMaxBytes = 256 << 10
	defaultMaxFiles = 32
)

type ReportInput struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
}

type Status struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Report struct {
	ID        string                `json:"id"`
	CreatedAt time.Time             `json:"created_at"`
	Version   string                `json:"version"`
	Platform  string                `json:"platform"`
	Arch      string                `json:"arch"`
	Severity  string                `json:"severity"`
	Category  string                `json:"category"`
	Summary   string                `json:"summary"`
	Events    []observability.Event `json:"events,omitempty"`
}

type queuedReport struct {
	Report      Report    `json:"report"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Config struct {
	Enabled        bool
	Endpoint       string
	QueueDir       string
	MaxReportBytes int64
	MaxQueueFiles  int
	RetryBase      time.Duration
	RetryMax       time.Duration
	Clock          func() time.Time
	Version        string
	Events         func() []observability.Event
	HTTPClient     *http.Client
}

type Service struct {
	config Config
	mu     sync.Mutex
}

func New(config Config) (*Service, error) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.MaxReportBytes == 0 {
		config.MaxReportBytes = defaultMaxBytes
	}
	if config.MaxQueueFiles == 0 {
		config.MaxQueueFiles = defaultMaxFiles
	}
	if config.RetryBase == 0 {
		config.RetryBase = 5 * time.Second
	}
	if config.RetryMax == 0 {
		config.RetryMax = 10 * time.Minute
	}
	if config.MaxReportBytes < 8<<10 || config.MaxReportBytes > 1<<20 || config.MaxQueueFiles < 1 || config.MaxQueueFiles > 1000 || config.RetryBase < 0 || config.RetryMax < config.RetryBase {
		return nil, ErrInvalidConfig
	}
	if strings.TrimSpace(config.QueueDir) == "" {
		return nil, ErrInvalidConfig
	}
	if config.Endpoint != "" {
		if err := validateEndpoint(config.Endpoint); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(config.QueueDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create queue", ErrQueue)
	}
	if err := os.Chmod(config.QueueDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: restrict queue", ErrQueue)
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Service{config: config}, nil
}

func (s *Service) Submit(ctx context.Context, input ReportInput) (Status, error) {
	if s == nil || !s.config.Enabled {
		return Status{}, ErrDisabled
	}
	if err := validateInput(input); err != nil {
		return Status{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now()
	report := Report{ID: newID(now), CreatedAt: now, Version: safeVersion(s.config.Version), Platform: runtime.GOOS, Arch: runtime.GOARCH, Severity: normalizeSeverity(input.Severity), Category: normalizeCategory(input.Category), Summary: sanitizeSummary(input.Summary), Events: s.events()}
	queued := queuedReport{Report: report, UpdatedAt: now}
	if err := s.write(report.ID, queued); err != nil {
		return Status{}, err
	}
	if s.config.Endpoint != "" {
		if err := s.flushOne(ctx, report.ID); err == nil {
			return Status{ID: report.ID, State: "submitted", CreatedAt: report.CreatedAt, UpdatedAt: now}, nil
		}
	}
	return s.statusFor(queuedPath(s.config.QueueDir, report.ID)), nil
}

func (s *Service) Flush(ctx context.Context) error {
	if s == nil || !s.config.Enabled {
		return ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	files, err := s.queueFiles()
	if err != nil {
		return err
	}
	for _, path := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_ = s.flushOne(ctx, strings.TrimSuffix(filepath.Base(path), ".json"))
	}
	return nil
}

func (s *Service) Pending() ([]Status, error) {
	if s == nil || !s.config.Enabled {
		return nil, ErrDisabled
	}
	files, err := s.queueFiles()
	if err != nil {
		return nil, err
	}
	result := make([]Status, 0, len(files))
	for _, path := range files {
		if status, readErr := s.statusForFile(path); readErr == nil {
			result = append(result, status)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Service) flushOne(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := queuedPath(s.config.QueueDir, id)
	queued, err := readQueued(path)
	if err != nil {
		return err
	}
	now := s.now()
	if !queued.NextAttempt.IsZero() && queued.NextAttempt.After(now) {
		return nil
	}
	if s.config.Endpoint == "" {
		return nil
	}
	payload, err := json.Marshal(queued.Report)
	if err != nil {
		return ErrUpload
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return ErrUpload
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "chuzi-diagnostics/1")
	response, err := s.config.HTTPClient.Do(request)
	if err == nil {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	}
	if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("%w: remove sent report", ErrQueue)
		}
		return nil
	}
	queued.Attempts++
	queued.UpdatedAt = now
	queued.NextAttempt = now.Add(backoff(s.config.RetryBase, s.config.RetryMax, queued.Attempts))
	if writeErr := s.write(id, queued); writeErr != nil {
		return writeErr
	}
	return ErrUpload
}

func (s *Service) write(id string, queued queuedReport) error {
	raw, err := json.Marshal(queued)
	if err != nil || int64(len(raw)) > s.config.MaxReportBytes {
		return ErrInvalidReport
	}
	path, temp := queuedPath(s.config.QueueDir, id), queuedPath(s.config.QueueDir, id)+".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open queue item", ErrQueue)
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("%w: write queue item", ErrQueue)
	}
	if err := os.Chmod(temp, 0o600); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("%w: restrict queue item", ErrQueue)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("%w: commit queue item", ErrQueue)
	}
	return nil
}

func (s *Service) queueFiles() ([]string, error) {
	entries, err := os.ReadDir(s.config.QueueDir)
	if err != nil {
		return nil, fmt.Errorf("%w: list queue", ErrQueue)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			files = append(files, filepath.Join(s.config.QueueDir, entry.Name()))
		}
	}
	sort.Strings(files)
	if len(files) > s.config.MaxQueueFiles {
		files = files[:s.config.MaxQueueFiles]
	}
	return files, nil
}

func (s *Service) statusFor(path string) Status {
	status, err := s.statusForFile(path)
	if err != nil {
		return Status{State: "queued"}
	}
	return status
}
func (s *Service) statusForFile(path string) (Status, error) {
	queued, err := readQueued(path)
	if err != nil {
		return Status{}, err
	}
	return Status{ID: queued.Report.ID, State: "queued", Attempts: queued.Attempts, CreatedAt: queued.Report.CreatedAt, UpdatedAt: queued.UpdatedAt}, nil
}
func (s *Service) events() []observability.Event {
	if s.config.Events == nil {
		return nil
	}
	items := s.config.Events()
	if len(items) > 64 {
		items = items[len(items)-64:]
	}
	result := make([]observability.Event, 0, len(items))
	for _, item := range items {
		result = append(result, observability.SanitizeEvent(item, s.now()))
	}
	return result
}
func (s *Service) now() time.Time {
	now := s.config.Clock()
	if now.IsZero() {
		now = time.Unix(0, 0)
	}
	return now.UTC()
}

func readQueued(path string) (queuedReport, error) {
	var queued queuedReport
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return queued, ErrQueue
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &queued) != nil || queued.Report.ID == "" {
		return queued, ErrQueue
	}
	return queued, nil
}
func queuedPath(dir, id string) string { return filepath.Join(dir, id+".json") }
func validateInput(input ReportInput) error {
	if normalizeSeverity(input.Severity) == "" || sanitizeSummary(input.Summary) == "" {
		return ErrInvalidReport
	}
	return nil
}
func normalizeSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SeverityWarning:
		return SeverityWarning
	case SeverityError:
		return SeverityError
	default:
		return ""
	}
}
func normalizeCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "core", "rdp", "ui", "browser", "storage", "network", "diagnostics":
		return value
	default:
		return "unknown"
	}
}
func sanitizeSummary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"password", "passwd", "token", "cookie", "authorization", "credential", "secret", "private key", "profile path"} {
		if strings.Contains(lower, marker) {
			return "user-visible diagnostic message redacted"
		}
	}
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			builder.WriteRune(' ')
		} else {
			builder.WriteRune(r)
		}
		if builder.Len() >= 240 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}
func safeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "unknown"
		}
	}
	return value
}
func validateEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return ErrInvalidConfig
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
	}
	return ErrInvalidConfig
}
func backoff(base, max time.Duration, attempts int) time.Duration {
	if attempts < 1 || base <= 0 {
		return 0
	}
	delay := base
	for index := 1; index < attempts && delay < max; index++ {
		if delay > max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}
func newID(now time.Time) string {
	var random [4]byte
	_, _ = rand.Read(random[:])
	return fmt.Sprintf("diag-%d-%s", now.UnixNano(), hex.EncodeToString(random[:]))
}
