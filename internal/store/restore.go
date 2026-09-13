package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Semcosm/chuzi/internal/config"
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
	if err := ValidateBackup(backupPath); err != nil {
		return err
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
	if _, statErr := os.Lstat(oldPath); statErr == nil {
		if err := removeRestoreBackup(oldPath); err != nil {
			return fmt.Errorf("%w: remove stale previous database: %v", ErrRestoreFailed, err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect stale previous database: %v", ErrRestoreFailed, statErr)
	}
	if existing, statErr := os.Lstat(destination); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidRestore
		}
		if err := os.Rename(destination, oldPath); err != nil {
			return fmt.Errorf("%w: stage existing database: %v", ErrRestoreFailed, err)
		}
	}
	if err := os.Rename(stagePath, destination); err != nil {
		_ = os.Rename(oldPath, destination)
		return fmt.Errorf("%w: install restored database: %v", ErrRestoreFailed, err)
	}
	if err := removeRestoreBackup(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The new database is already installed and validated. Keep the prior
		// file as a recoverable rollback artifact rather than reporting a
		// failed restore that cannot be distinguished from a partial install.
		return fmt.Errorf("%w: restored database installed; previous database retained at %s: %v", ErrRestoreFailed, oldPath, err)
	}
	return nil
}

func removeRestoreBackup(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidRestore
	}
	return os.Remove(path)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
