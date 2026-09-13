package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	ErrInvalidLogger = errors.New("observability: invalid logger")
	ErrLogWrite      = errors.New("observability: log write failed")
)

// LoggerConfig controls a JSONL logger. Writer is useful for tests and for a
// supervisor-managed stdout/stderr stream. Path enables an owner-only file
// with bounded rotation; exactly one of Writer and Path may be supplied.
type LoggerConfig struct {
	Writer   io.Writer
	Path     string
	MaxBytes int64
	MaxFiles int
	Clock    func() time.Time
}

const (
	defaultMaxLogBytes = 10 << 20
	defaultMaxLogFiles = 5
)

// JSONLogger writes one structured, redacted record per line. It implements
// Sink for fire-and-forget event paths and exposes Write for callers that need
// to observe I/O errors. No caller-provided free-form message is accepted.
type JSONLogger struct {
	mu       sync.Mutex
	writer   io.Writer
	file     *os.File
	path     string
	maxBytes int64
	maxFiles int
	clock    func() time.Time
	bytes    int64
	lastErr  error
	closed   bool
}

// NewJSONLogger creates a structured logger. File logs are created with 0600
// permissions and parent directories with 0700 where possible.
func NewJSONLogger(config LoggerConfig) (*JSONLogger, error) {
	if config.Writer != nil && strings.TrimSpace(config.Path) != "" {
		return nil, fmt.Errorf("%w: writer and path are mutually exclusive", ErrInvalidLogger)
	}
	if config.Writer == nil && strings.TrimSpace(config.Path) == "" {
		return nil, fmt.Errorf("%w: writer or path is required", ErrInvalidLogger)
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = defaultMaxLogBytes
	}
	if config.MaxFiles == 0 {
		config.MaxFiles = defaultMaxLogFiles
	}
	if config.MaxBytes < 0 || config.MaxFiles < 1 {
		return nil, fmt.Errorf("%w: rotation limits must be positive", ErrInvalidLogger)
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	path := strings.TrimSpace(config.Path)
	logger := &JSONLogger{
		writer:   config.Writer,
		path:     "",
		maxBytes: config.MaxBytes,
		maxFiles: config.MaxFiles,
		clock:    config.Clock,
	}
	if path != "" {
		logger.path = filepath.Clean(path)
		if err := os.MkdirAll(filepath.Dir(logger.path), 0o700); err != nil {
			return nil, fmt.Errorf("%w: create log directory: %v", ErrInvalidLogger, err)
		}
		if info, err := os.Lstat(logger.path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%w: log path must be a regular file", ErrInvalidLogger)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: inspect log file: %v", ErrInvalidLogger, err)
		}
		file, err := os.OpenFile(logger.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("%w: open log file: %v", ErrInvalidLogger, err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("%w: restrict log file: %v", ErrInvalidLogger, err)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("%w: stat log file: %v", ErrInvalidLogger, statErr)
		}
		logger.file, logger.writer, logger.bytes = file, file, info.Size()
	}
	return logger, nil
}

// NewLogger is a concise compatibility alias for NewJSONLogger.
func NewLogger(config LoggerConfig) (*JSONLogger, error) { return NewJSONLogger(config) }

// Record implements Sink. I/O failures are retained in LastError and never
// become a panic or a process-wide log recursion.
func (l *JSONLogger) Record(event Event) {
	if l == nil {
		return
	}
	if err := l.Write(event); err != nil {
		l.mu.Lock()
		l.lastErr = err
		l.mu.Unlock()
	}
}

// Write appends one redacted JSON object followed by a newline.
func (l *JSONLogger) Write(event Event) error {
	if l == nil {
		return ErrInvalidLogger
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrLogWrite
	}
	if l.writer == nil {
		return ErrInvalidLogger
	}
	record := sanitizeEvent(event, l.clock())
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrLogWrite, err)
	}
	line = append(line, '\n')
	if l.path != "" && l.maxBytes > 0 && l.bytes > 0 && l.bytes+int64(len(line)) > l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			return err
		}
	}
	n, err := l.writer.Write(line)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLogWrite, err)
	}
	if n != len(line) {
		return fmt.Errorf("%w: short write", ErrLogWrite)
	}
	l.bytes += int64(n)
	return nil
}

// LastError returns the most recent asynchronous Record failure, if any.
func (l *JSONLogger) LastError() error {
	if l == nil {
		return ErrInvalidLogger
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}

// Close flushes and closes a file-backed logger. Writer-backed loggers are
// marked closed but are not closed because ownership remains with the caller.
func (l *JSONLogger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

func (l *JSONLogger) rotateLocked() error {
	if l.file == nil || l.path == "" {
		return nil
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("%w: close rotated log: %v", ErrLogWrite, err)
	}
	for index := l.maxFiles - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", l.path, index)
		to := fmt.Sprintf("%s.%d", l.path, index+1)
		if err := prepareRotationSource(from); err == nil {
			if index+1 >= l.maxFiles {
				if err := removeRotationTarget(to); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("%w: remove rotated log: %v", ErrLogWrite, err)
				}
			}
			if err := os.Rename(from, to); err != nil {
				return fmt.Errorf("%w: rotate %s: %v", ErrLogWrite, from, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect rotation: %v", ErrLogWrite, err)
		}
	}
	if err := prepareRotationSource(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect active log: %v", ErrLogWrite, err)
	}
	if l.maxFiles == 1 {
		if err := removeRotationTarget(l.path + ".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: remove rotated log: %v", ErrLogWrite, err)
		}
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: archive log: %v", ErrLogWrite, err)
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: reopen log: %v", ErrLogWrite, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("%w: restrict reopened log: %v", ErrLogWrite, err)
	}
	l.file, l.writer, l.bytes = file, file, 0
	return nil
}

func prepareRotationSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("rotation path must be a regular file: %s", path)
	}
	return os.Chmod(path, 0o600)
}

func removeRotationTarget(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("rotation path must be a regular file: %s", path)
	}
	return os.Remove(path)
}

type logRecord struct {
	At         string `json:"at"`
	Component  string `json:"component,omitempty"`
	Operation  string `json:"operation,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Resource   string `json:"resource,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

func sanitizeEvent(event Event, fallback time.Time) logRecord {
	at := event.At
	if at.IsZero() {
		at = fallback
	}
	if at.IsZero() {
		at = time.Unix(0, 0).UTC()
	}
	record := logRecord{
		At:         at.UTC().Format(time.RFC3339Nano),
		Component:  safeField(event.Component),
		Operation:  safeField(event.Operation),
		Outcome:    safeField(event.Outcome),
		RequestID:  safeResource(event.RequestID),
		Resource:   safeResource(event.Resource),
		ErrorClass: safeField(event.ErrorClass),
	}
	if event.Duration > 0 {
		record.DurationMS = event.Duration.Milliseconds()
		if record.DurationMS == 0 {
			record.DurationMS = 1
		}
	}
	return record
}

func safeField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) || (char != '_' && char != '-' && char != '.' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9')) {
			return ""
		}
	}
	return value
}

func safeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return ""
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return ""
		}
	}
	return value
}

func safeResource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return RedactIdentifier(value)
}

// SortedRotationFiles is a small diagnostic helper used by operators and
// tests. It returns only regular files belonging to the logger's path.
func SortedRotationFiles(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidLogger
	}
	entries, err := filepath.Glob(filepath.Clean(path) + "*")
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, statErr := os.Lstat(entry)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			result = append(result, entry)
		}
	}
	sort.Strings(result)
	return result, nil
}
