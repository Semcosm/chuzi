package browser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Semcosm/chuzi/internal/config"
)

func TestProfilesRemoveAndRetainAreBoundedAndIdempotent(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := profiles.Prepare("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "marker"), []byte("profile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := profiles.Retain("account-1"); err != nil {
		t.Fatalf("Retain() = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retained profile = %v", err)
	}
	_, release, err := profiles.Acquire("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Remove("account-1"); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("Remove() while active = %v", err)
	}
	if err := profiles.Retain("account-1"); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("Retain() while active = %v", err)
	}
	release()
	if err := profiles.Remove("account-1"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed profile stat = %v", err)
	}
	if err := profiles.Remove("account-1"); err != nil {
		t.Fatalf("idempotent Remove() = %v", err)
	}
}

func TestProfilesRejectSymlinkedDeletionTarget(t *testing.T) {
	cfg, err := config.New(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := NewProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := profiles.Path("account-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := profiles.Remove("account-1"); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("Remove() symlink = %v", err)
	}
	if err := profiles.Retain("account-1"); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("Retain() symlink = %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside profile was affected: %v", err)
	}
}
