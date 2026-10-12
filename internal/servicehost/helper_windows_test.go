//go:build windows

package servicehost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestWindowsHelperStarterRejectsUnboundInputsBeforeWTS(t *testing.T) {
	runtime, err := NewTrustedRuntime(filepath.Join(t.TempDir(), "CorePayload"))
	if err != nil {
		t.Fatal(err)
	}
	starter := NewWindowsHelperStarter()
	if _, err := starter.Start(context.Background(), runtime, "operator", 7); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("invalid SID error = %v, want ErrHelperUnavailable", err)
	}
	if _, err := starter.Start(nil, runtime, "S-1-5-21-1", 7); !errors.Is(err, ErrHelperUnavailable) {
		t.Fatalf("nil context error = %v, want ErrHelperUnavailable", err)
	}
}
