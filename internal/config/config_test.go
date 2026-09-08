package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadNormalizesDataDirAndDerivesPaths(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"data_dir":"./runtime"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.DataDir) || got.DatabasePath() != filepath.Join(got.DataDir, DatabaseFileName) {
		t.Fatalf("unexpected normalized config: %#v", got)
	}
	backup, err := got.BackupPath(time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(backup) != got.BackupDir() || !strings.HasSuffix(backup, ".db") {
		t.Fatalf("unexpected backup path: %s", backup)
	}
}

func TestLoadRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		body string
	}{
		{"unknown-secret", `{"data_dir":"data","token":"must-not-be-accepted"}`},
		{"trailing-value", `{"data_dir":"data"}{"token":"secret"}`},
		{"empty-data-dir", `{"data_dir":"  "}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name+".json")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config") {
				t.Fatalf("Load() error = %v, want configuration error", err)
			}
		})
	}
}

func TestValidateRejectsRootAndRelativeLiteralPaths(t *testing.T) {
	for _, candidate := range []Config{{DataDir: "/"}, {DataDir: "relative"}} {
		if err := candidate.Validate(); err == nil {
			t.Errorf("config %#v should be invalid", candidate)
		}
	}
}
