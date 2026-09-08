// Package config defines deployment configuration and derives all runtime
// storage paths from the configured data directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DatabaseFileName = "chuzi.db"
	BackupDirectory  = "backups"
)

var ErrInvalidConfig = errors.New("config: invalid configuration")

// Config contains ordinary deployment settings only. Secrets intentionally do
// not have a field in this type and must be supplied by a later secret store.
type Config struct {
	DataDir string `json:"data_dir"`
}

// New validates and normalizes a deployment data directory. Relative paths are
// resolved against the process working directory once, at configuration load.
func New(dataDir string) (Config, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return Config{}, fmt.Errorf("%w: data_dir is required", ErrInvalidConfig)
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("%w: resolve data_dir: %v", ErrInvalidConfig, err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == string(filepath.Separator) {
		return Config{}, fmt.Errorf("%w: data_dir must not be the filesystem root", ErrInvalidConfig)
	}
	return Config{DataDir: absolute}, nil
}

// Load reads a JSON configuration and rejects unknown fields and trailing
// content. This keeps accidental secret/configuration drift visible.
func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("%w: config path is required", ErrInvalidConfig)
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var raw Config
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidConfig)
		}
		return Config{}, fmt.Errorf("%w: trailing content: %v", ErrInvalidConfig, err)
	}
	return New(raw.DataDir)
}

// Validate checks a Config value, including values constructed as a struct
// literal rather than through New or Load.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" || !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("%w: data_dir must be a non-empty absolute path", ErrInvalidConfig)
	}
	if filepath.Clean(c.DataDir) == string(filepath.Separator) {
		return fmt.Errorf("%w: data_dir must not be the filesystem root", ErrInvalidConfig)
	}
	return nil
}

// DatabasePath derives the only database path used by the service.
func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, DatabaseFileName)
}

// BackupDir derives the only backup directory used by the service.
func (c Config) BackupDir() string {
	return filepath.Join(c.DataDir, BackupDirectory)
}

// BackupPath derives a timestamped backup path from an injected UTC time.
func (c Config) BackupPath(at time.Time) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if at.IsZero() {
		return "", fmt.Errorf("%w: backup time is required", ErrInvalidConfig)
	}
	name := "chuzi-" + at.UTC().Format("20060102T150405.000000000Z") + ".db"
	return filepath.Join(c.BackupDir(), name), nil
}
