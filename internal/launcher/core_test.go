package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
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
