package launcher

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFileLockExcludesConcurrentOperationsAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".chuzi", "launcher.lock")
	first, err := AcquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireFileLock(context.Background(), path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second lock = %v, want ErrLockHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("idempotent release = %v", err)
	}
	second, err := AcquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFileLockHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AcquireFileLock(ctx, filepath.Join(t.TempDir(), "lock")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock = %v, want context.Canceled", err)
	}
}
