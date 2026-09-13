package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileLock is an inter-process installation lock. It uses O_EXCL so another
// launcher cannot enter a mutating operation between a check and a rename.
// Stale locks are intentionally not broken automatically; an operator must
// verify that the recorded process is gone before removing one.
type FileLock struct {
	path string
	file *os.File
	once sync.Once
	err  error
}

type lockRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func AcquireFileLock(ctx context.Context, path string) (*FileLock, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: lock path must be an absolute clean path", ErrInvalidPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create launcher lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("create launcher lock: %w", err)
	}
	record := lockRecord{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write launcher lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync launcher lock: %w", err)
	}
	return &FileLock{path: path, file: file}, nil
}

// Release is idempotent. It closes the descriptor before removing the file so
// the same operation works on Windows where open files cannot be unlinked.
func (l *FileLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file != nil {
			l.err = l.file.Close()
		}
		if l.err == nil {
			l.err = os.Remove(l.path)
			if os.IsNotExist(l.err) {
				l.err = nil
			}
		}
	})
	return l.err
}
