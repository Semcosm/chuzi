// Package config defines deployment configuration and derives all runtime
// storage paths from the configured data directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	DataDir     string           `json:"data_dir"`
	Matrix      MatrixConfig     `json:"matrix,omitempty"`
	Credentials CredentialConfig `json:"credentials,omitempty"`
	Health      HealthConfig     `json:"health,omitempty"`
}

// MatrixConfig contains non-secret Matrix deployment settings. The access
// token is read from AccessTokenEnv and is never accepted in this file.
type MatrixConfig struct {
	HomeserverURL      string                       `json:"homeserver_url,omitempty"`
	UserID             string                       `json:"user_id,omitempty"`
	AccessTokenEnv     string                       `json:"access_token_env,omitempty"`
	SyncEnabled        bool                         `json:"sync_enabled,omitempty"`
	SyncTimeoutSeconds int                          `json:"sync_timeout_seconds,omitempty"`
	PollIntervalMillis int                          `json:"poll_interval_millis,omitempty"`
	Rooms              map[string]map[string]string `json:"rooms,omitempty"`
}

// CredentialConfig names deployment environment variables for the keyring.
// Values are names only; key material must remain outside ordinary config.
type CredentialConfig struct {
	KeyEnv   string `json:"key_env,omitempty"`
	KeyIDEnv string `json:"key_id_env,omitempty"`
}

// HealthConfig controls the optional local health HTTP listener.
type HealthConfig struct {
	Listen string `json:"listen,omitempty"`
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
	normalized, err := New(raw.DataDir)
	if err != nil {
		return Config{}, err
	}
	normalized.Matrix = raw.Matrix
	normalized.Credentials = raw.Credentials
	normalized.Health = raw.Health
	if err := normalized.Validate(); err != nil {
		return Config{}, err
	}
	return normalized, nil
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
	if err := c.Matrix.Validate(); err != nil {
		return err
	}
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	if err := c.Health.Validate(); err != nil {
		return err
	}
	return nil
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (m MatrixConfig) Validate() error {
	if strings.TrimSpace(m.HomeserverURL) == "" {
		if strings.TrimSpace(m.UserID) != "" || strings.TrimSpace(m.AccessTokenEnv) != "" || m.SyncEnabled || len(m.Rooms) != 0 {
			return fmt.Errorf("%w: matrix homeserver_url is required when Matrix is configured", ErrInvalidConfig)
		}
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(m.HomeserverURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: matrix homeserver_url must be an http(s) origin", ErrInvalidConfig)
	}
	if strings.TrimSpace(m.AccessTokenEnv) == "" || !envNamePattern.MatchString(m.AccessTokenEnv) {
		return fmt.Errorf("%w: matrix valid access_token_env is required", ErrInvalidConfig)
	}
	if m.SyncEnabled && strings.TrimSpace(m.UserID) == "" {
		return fmt.Errorf("%w: matrix user_id is required when sync is enabled", ErrInvalidConfig)
	}
	if m.SyncTimeoutSeconds < 0 || m.SyncTimeoutSeconds > 300 || m.PollIntervalMillis < 0 || m.PollIntervalMillis > 60000 {
		return fmt.Errorf("%w: matrix sync timing is out of range", ErrInvalidConfig)
	}
	for room, users := range m.Rooms {
		if strings.TrimSpace(room) == "" || len(room) > 512 {
			return fmt.Errorf("%w: matrix room is invalid", ErrInvalidConfig)
		}
		for user, role := range users {
			if strings.TrimSpace(user) == "" || (role != "user" && role != "admin") {
				return fmt.Errorf("%w: matrix room policy is invalid", ErrInvalidConfig)
			}
		}
	}
	return nil
}

func (c CredentialConfig) Validate() error {
	for _, name := range []string{strings.TrimSpace(c.KeyEnv), strings.TrimSpace(c.KeyIDEnv)} {
		if name != "" && !envNamePattern.MatchString(name) {
			return fmt.Errorf("%w: credential environment name is invalid", ErrInvalidConfig)
		}
	}
	return nil
}

func (h HealthConfig) Validate() error {
	if strings.TrimSpace(h.Listen) == "" {
		return nil
	}
	if _, _, err := net.SplitHostPort(strings.TrimSpace(h.Listen)); err != nil {
		return fmt.Errorf("%w: health listen must be host:port", ErrInvalidConfig)
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
