//go:build windows

package launcher

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Semcosm/chuzi/internal/servicehost"
)

func TestCoreManagerRejectsUntrustedWindowsRoot(t *testing.T) {
	if _, err := NewCoreManager(t.TempDir()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("NewCoreManager() error = %v, want ErrInvalidPath", err)
	}
}

func TestCoreManagerWithRuntimeRequiresFixedDataRoot(t *testing.T) {
	runtime, err := servicehost.NewTrustedRuntime(filepath.Join(t.TempDir(), "CorePayload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCoreManagerWithRuntime(t.TempDir(), runtime.Root); !errors.Is(err, servicehost.ErrInvalidConfig) {
		t.Fatalf("NewCoreManagerWithRuntime() error = %v, want servicehost.ErrInvalidConfig", err)
	}
}
