//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteServiceHostDiagnosticDropsUntrustedFieldText(t *testing.T) {
	root := t.TempDir()
	event := serviceHostDiagnosticEvent{
		Stage:      "runtime assembly C:\\private\\profile",
		Outcome:    "failed\npassword=secret",
		ErrorClass: "token=secret",
		Control:    "C:\\private\\profile",
	}
	if err := writeServiceHostDiagnostic(root, event); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, serviceHostDiagnosticName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "private") {
		t.Fatalf("diagnostic leaked raw field text: %s", data)
	}
	var stored storedServiceHostDiagnostic
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode diagnostic: %v", err)
	}
	if stored.Schema != serviceHostDiagnosticSchema || stored.Stage != "unknown" || stored.Outcome != "unknown" || stored.ErrorClass != "" || stored.Control != "" {
		t.Fatalf("stored diagnostic = %#v", stored)
	}
	if stored.At.IsZero() {
		t.Fatal("diagnostic timestamp was not set")
	}
}

func TestServiceHostDiagnosticRejectsNonAbsoluteRoot(t *testing.T) {
	if err := writeServiceHostDiagnostic("relative", serviceHostDiagnosticEvent{Stage: "service_state", Outcome: "running"}); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("relative diagnostic root error = %v, want invalid path", err)
	}
}
