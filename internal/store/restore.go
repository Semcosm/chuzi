package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrInvalidRestore = errors.New("store: invalid restore source")
	ErrRestoreFailed  = errors.New("store: restore failed")
)

// Restore atomically replaces the configured database with a validated backup.
// The service must be stopped before calling Restore; the function rejects
// symlinks and paths outside the config-derived backup directory.
func Restore(cfg config.Config, backupPath string) error {
	normalized, err := config.New(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := normalized.Validate(); err != nil {
		return err
	}
	backupPath, err = filepath.Abs(strings.TrimSpace(backupPath))
	if err != nil || !within(normalized.BackupDir(), backupPath) {
		return ErrInvalidRestore
	}
	info, err := os.Lstat(backupPath)
	if err != nil {
		return fmt.Errorf("%w: inspect backup: %v", ErrInvalidRestore, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidRestore
	}
	// Windows reports ACL-backed files with synthetic Unix permission bits;
	// the mode argument cannot enforce 0600 there. Unix files must remain
	// owner-only because that is the platform's permission boundary.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return ErrInvalidRestore
	}
	backup, err := bbolt.Open(backupPath, 0o600, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("%w: open backup: %v", ErrInvalidRestore, err)
	}
	version, versionErr := migrations.Version(backup)
	closeErr := backup.Close()
	if versionErr != nil || closeErr != nil || version != migrations.CurrentVersion {
		return fmt.Errorf("%w: backup schema is not current", ErrInvalidRestore)
	}
	if err := os.MkdirAll(normalized.DataDir, 0o700); err != nil {
		return fmt.Errorf("%w: create data directory: %v", ErrRestoreFailed, err)
	}
	stage, err := os.CreateTemp(normalized.DataDir, ".chuzi-restore-")
	if err != nil {
		return fmt.Errorf("%w: stage backup: %v", ErrRestoreFailed, err)
	}
	stagePath := stage.Name()
	defer os.Remove(stagePath)
	if err := stage.Chmod(0o600); err != nil {
		_ = stage.Close()
		return fmt.Errorf("%w: restrict staged backup: %v", ErrRestoreFailed, err)
	}
	source, err := os.Open(backupPath)
	if err != nil {
		_ = stage.Close()
		return fmt.Errorf("%w: read backup: %v", ErrRestoreFailed, err)
	}
	_, copyErr := io.Copy(stage, source)
	_ = source.Close()
	if copyErr == nil {
		copyErr = stage.Sync()
	}
	if closeErr := stage.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("%w: stage backup: %v", ErrRestoreFailed, copyErr)
	}
	destination := normalized.DatabasePath()
	oldPath := destination + ".before-restore"
	_ = os.Remove(oldPath)
	if existing, statErr := os.Lstat(destination); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidRestore
		}
		if err := os.Rename(destination, oldPath); err != nil {
			return fmt.Errorf("%w: stage existing database: %v", ErrRestoreFailed, err)
		}
	}
	if err := os.Rename(stagePath, destination); err != nil {
		if _, statErr := os.Stat(oldPath); statErr == nil {
			_ = os.Rename(oldPath, destination)
		}
		return fmt.Errorf("%w: install restored database: %v", ErrRestoreFailed, err)
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove previous database: %v", ErrRestoreFailed, err)
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
