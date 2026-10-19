//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/servicehost"
	"github.com/Semcosm/chuzi/internal/store"
)

const (
	serviceHostDiagnosticSchema = "chuzi.service-host-diagnostic/v1"
	serviceHostDiagnosticName   = "service-host-diagnostics.jsonl"
	serviceHostDiagnosticLimit  = 64 * 1024
)

var serviceHostDiagnosticMu sync.Mutex

type serviceHostDiagnosticEvent struct {
	Stage      string `json:"stage"`
	Outcome    string `json:"outcome"`
	ErrorClass string `json:"error_class,omitempty"`
	Control    string `json:"control,omitempty"`
}

type storedServiceHostDiagnostic struct {
	Schema string    `json:"schema"`
	At     time.Time `json:"at"`
	serviceHostDiagnosticEvent
}

func recordServiceHostDiagnostic(event serviceHostDiagnosticEvent) {
	root, err := servicehost.FixedDataRoot()
	if err != nil {
		return
	}
	_ = writeServiceHostDiagnostic(root, event)
}

func writeServiceHostDiagnostic(root string, event serviceHostDiagnosticEvent) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return os.ErrInvalid
	}
	event.Stage = safeServiceDiagnosticToken(event.Stage, "unknown")
	event.Outcome = safeServiceDiagnosticToken(event.Outcome, "unknown")
	event.ErrorClass = safeServiceDiagnosticToken(event.ErrorClass, "")
	event.Control = safeServiceDiagnosticToken(event.Control, "")
	line, err := json.Marshal(storedServiceHostDiagnostic{
		Schema:                     serviceHostDiagnosticSchema,
		At:                         time.Now().UTC(),
		serviceHostDiagnosticEvent: event,
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')

	serviceHostDiagnosticMu.Lock()
	defer serviceHostDiagnosticMu.Unlock()
	path := filepath.Join(root, serviceHostDiagnosticName)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr == nil && info.Size()+int64(len(line)) > serviceHostDiagnosticLimit {
		if err := file.Close(); err != nil {
			return err
		}
		_ = os.Remove(path + ".1")
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
		file, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
	} else if statErr != nil {
		_ = file.Close()
		return statErr
	}
	defer file.Close()
	written, err := file.Write(line)
	if err != nil {
		return err
	}
	if written != len(line) {
		return errors.New("short service diagnostic write")
	}
	return file.Sync()
}

func safeServiceDiagnosticToken(value, fallback string) string {
	if value == "" {
		return fallback
	}
	if len(value) > 64 {
		return fallback
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return fallback
		}
	}
	return value
}

func serviceHostErrorClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, config.ErrInvalidConfig), errors.Is(err, servicehost.ErrInvalidConfig):
		return "invalid_configuration"
	case errors.Is(err, servicehost.ErrIncompatibleService):
		return "incompatible_service"
	case errors.Is(err, servicehost.ErrPreflight):
		return "preflight_failed"
	case errors.Is(err, servicehost.ErrHostUnavailable):
		return "host_unavailable"
	case errors.Is(err, store.ErrCorruptData):
		return "database_corrupt"
	case errors.Is(err, credential.ErrKeyUnavailable):
		return "credential_key_unavailable"
	case errors.Is(err, os.ErrNotExist):
		return "required_file_missing"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		return "startup_failed"
	}
}
