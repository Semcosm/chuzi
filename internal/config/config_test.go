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

func TestLoadDeploymentSettingsKeepSecretsOutOfConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	body := `{"data_dir":"runtime","matrix":{"homeserver_url":"https://matrix.example.org","user_id":"@bot:example.org","access_token_env":"MATRIX_TOKEN","sync_enabled":true,"rooms":{"!ops:example.org":{"@alice:example.org":"user"}}},"credentials":{"key_env":"CRED_KEY","key_id_env":"CRED_KEY_ID"},"health":{"listen":"127.0.0.1:8080"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Matrix.HomeserverURL == "" || got.Matrix.AccessTokenEnv != "MATRIX_TOKEN" || got.Credentials.KeyEnv != "CRED_KEY" {
		t.Fatalf("deployment settings not loaded: %#v", got)
	}
}

func TestLoadRejectsMatrixTokenInConfigAndInvalidEnvironmentNames(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		`{"data_dir":"runtime","matrix":{"homeserver_url":"https://matrix.example.org","user_id":"@bot:example.org","access_token":"secret","access_token_env":"MATRIX_TOKEN"}}`,
		`{"data_dir":"runtime","credentials":{"key_env":"BAD-NAME"}}`,
	}
	for index, body := range cases {
		path := filepath.Join(root, string(rune('a'+index))+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("Load(%q) unexpectedly succeeded", body)
		}
	}
}
