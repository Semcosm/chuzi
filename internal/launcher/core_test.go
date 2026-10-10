package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Semcosm/chuzi/internal/config"
)

func TestCoreManagerReportsNotInstalledWithoutTouchingCore(t *testing.T) {
	manager, err := NewCoreManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed || status.Ready || status.Running || status.Status != "not_installed" {
		t.Fatalf("status = %#v, want not_installed and inactive", status)
	}
}

func TestCoreManagerRejectsInvalidProxyPayloadBeforeDialing(t *testing.T) {
	manager := &CoreManager{Root: filepath.Clean(t.TempDir())}
	_, err := manager.Call(context.Background(), "get_account", json.RawMessage(`{"account_id":`))
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Call() error = %v, want ErrInvalidPath", err)
	}
}

func TestCoreRestartsPreserveDeploymentConfiguration(t *testing.T) {
	manager, _ := NewCoreManager(t.TempDir())
	if err := manager.writeConfig(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manager.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Credentials.KeyEnv = "DEPLOYMENT_KEY"
	cfg.JobPool = config.JobPoolConfig{PoolID: "pool-a", EnvironmentID: "env-a", EnvironmentVersion: "1.0.0", DesiredSlots: 0, ManifestDigest: strings.Repeat("a", 64), Signer: "signer", RequireTrusted: true}
	cfg.EnvironmentPackage = config.EnvironmentPackageConfig{EnvironmentID: "env-a", Version: "1.0.0", ManifestDigest: strings.Repeat("a", 64), Signer: "signer"}
	cfg.WindowsJobPool = config.WindowsJobPoolConfig{Enabled: true, UserPrefix: "ChuziJob", EnvironmentID: "env-a", EnvironmentVersion: "1.0.0", AgentHeartbeatSeconds: 5, ProvisionTimeoutSeconds: 120, CleanupTimeoutSeconds: 120}
	if err := config.Save(manager.ConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(manager.ConfigPath)
	for i := 0; i < 2; i++ {
		if err := manager.writeConfig(); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.ReadFile(manager.ConfigPath)
	if string(before) != string(after) {
		t.Fatal("restart overwrote deployment settings")
	}
	status, err := manager.Status(context.Background())
	if err != nil || status.ConfiguredPoolMode != "windows" {
		t.Fatalf("status: %#v %v", status, err)
	}
}

func TestCoreRejectsBrokenOrForeignConfigWithoutOverwriting(t *testing.T) {
	for _, content := range []string{
		"{", "{} {}", "{\"data_dir\":\"/\",\"unknown\":true}",
	} {
		t.Run(content, func(t *testing.T) {
			manager, _ := NewCoreManager(t.TempDir())
			os.WriteFile(manager.ConfigPath, []byte(content), 0600)
			if err := manager.writeConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
			after, _ := os.ReadFile(manager.ConfigPath)
			if string(after) != content {
				t.Fatal("invalid config overwritten")
			}
			status, _ := manager.Status(context.Background())
			if status.ConfiguredPoolMode != "unknown" {
				t.Fatalf("mode=%s", status.ConfiguredPoolMode)
			}
		})
	}
	manager, _ := NewCoreManager(t.TempDir())
	foreign, _ := config.New(t.TempDir())
	if err := config.Save(manager.ConfigPath, foreign); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeConfig(); err == nil {
		t.Fatal("foreign data root accepted")
	}
}
