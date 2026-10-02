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
	for _, candidate := range []Config{{DataDir: "/"}, {DataDir: "relative"}, {DataDir: "/tmp/chuzi\nstate"}, {DataDir: "/tmp/chuzi\x00state"}} {
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

func TestConfigAcceptsRedactionSafeObservabilitySettings(t *testing.T) {
	cfg, err := New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Observability = ObservabilityConfig{MetricsListen: "127.0.0.1:9090", LogPath: filepath.Join(cfg.DataDir, "service.log"), LogMaxBytes: 1024, LogMaxFiles: 3}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Observability.MetricsListen = "not-an-address"
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid metrics listener was accepted")
	}
}

func TestConfigValidatesRequestRateLimits(t *testing.T) {
	cfg, err := New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.RateLimit = RateLimitConfig{GlobalLimit: 30, GlobalWindowSeconds: 60, ActorLimit: 10, ActorWindowSeconds: 60}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.RateLimit.GlobalWindowSeconds = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabled rate limit without a window was accepted")
	}
}

func TestConfigValidatesLogicalJobPoolAndTreatsAllMetadataAsEnabled(t *testing.T) {
	cfg, err := New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.JobPool = JobPoolConfig{RequireTrusted: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("trust-only job pool configuration was silently disabled")
	}
	cfg.JobPool = JobPoolConfig{
		PoolID: "pool-test", EnvironmentID: "chuzi-environment/v1",
		EnvironmentVersion: "1.0.0", DesiredSlots: 2, Capabilities: []string{"cdp"},
		ManifestDigest: "sha256:test", Signer: "signer", RequireTrusted: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.JobPool.Capabilities = []string{"cdp", "cdp"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate job pool capability was accepted")
	}
}

func TestWindowsJobPoolConfigIsSeparateAndConstrained(t *testing.T) {
	cfg, err := New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.JobPool = JobPoolConfig{PoolID: "pool", EnvironmentID: "env/v1", EnvironmentVersion: "1.0", DesiredSlots: 2}
	cfg.WindowsJobPool = WindowsJobPoolConfig{Enabled: true, DesiredSlots: 2, UserPrefix: "ChuziJob", RDPEnabled: true, SessionIdleTimeoutSeconds: 300, AgentHeartbeatSeconds: 10, ProvisionTimeoutSeconds: 120, CleanupTimeoutSeconds: 60, EnvironmentID: "env/v1", EnvironmentVersion: "1.0"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.WindowsJobPool.UserPrefix = "bad;prefix"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsafe user prefix accepted")
	}
	cfg.WindowsJobPool.UserPrefix = "ChuziJob"
	cfg.WindowsJobPool.DesiredSlots = 3
	if err := cfg.Validate(); err == nil {
		t.Fatal("logical and Windows pool capacity mismatch accepted")
	}
	cfg.WindowsJobPool.DesiredSlots = 2
	cfg.WindowsJobPool.AgentHeartbeatSeconds = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero agent heartbeat accepted")
	}
}
