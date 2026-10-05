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
	DataDir            string                   `json:"data_dir"`
	Matrix             MatrixConfig             `json:"matrix,omitempty"`
	Credentials        CredentialConfig         `json:"credentials,omitempty"`
	Health             HealthConfig             `json:"health,omitempty"`
	Observability      ObservabilityConfig      `json:"observability,omitempty"`
	Diagnostics        DiagnosticsConfig        `json:"diagnostics,omitempty"`
	RateLimit          RateLimitConfig          `json:"rate_limit,omitempty"`
	EnvironmentPackage EnvironmentPackageConfig `json:"environment_package,omitempty"`
	JobPool            JobPoolConfig            `json:"job_pool,omitempty"`
	WindowsJobPool     WindowsJobPoolConfig     `json:"windows_job_pool,omitempty"`
}

// EnvironmentPackageConfig identifies the service-owned manifest selected by
// a pool. It deliberately carries metadata only; package files are resolved
// below a deployment-owned root by the environment manager.
type EnvironmentPackageConfig struct {
	EnvironmentID  string `json:"environment_id,omitempty"`
	Version        string `json:"version,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	Signer         string `json:"signer,omitempty"`
}

func (e EnvironmentPackageConfig) Enabled() bool {
	return strings.TrimSpace(e.EnvironmentID) != "" || strings.TrimSpace(e.Version) != "" || strings.TrimSpace(e.ManifestDigest) != "" || strings.TrimSpace(e.Signer) != ""
}

func (e EnvironmentPackageConfig) Validate(pool JobPoolConfig) error {
	if !e.Enabled() {
		return nil
	}
	if !pool.Enabled() || e.EnvironmentID != pool.EnvironmentID || e.Version != pool.EnvironmentVersion || e.ManifestDigest != pool.ManifestDigest || e.Signer != pool.Signer {
		return fmt.Errorf("%w: environment_package must match job_pool", ErrInvalidConfig)
	}
	if strings.TrimSpace(e.EnvironmentID) != e.EnvironmentID || strings.TrimSpace(e.Version) != e.Version || strings.TrimSpace(e.ManifestDigest) != e.ManifestDigest || strings.TrimSpace(e.Signer) != e.Signer || strings.ContainsAny(e.EnvironmentID+e.Version+e.ManifestDigest+e.Signer, "\r\n\t") {
		return fmt.Errorf("%w: invalid environment_package metadata", ErrInvalidConfig)
	}
	return nil
}

// WindowsJobPoolConfig configures OS-backed job slots separately from launcher
// BehaviorSettings and the logical slot pool. Secrets and executable/path
// overrides are intentionally not representable here.
type WindowsJobPoolConfig struct {
	Enabled                   bool   `json:"enabled,omitempty"`
	DesiredSlots              int    `json:"desired_slots,omitempty"`
	UserPrefix                string `json:"user_prefix,omitempty"`
	RDPEnabled                bool   `json:"rdp_enabled,omitempty"`
	SessionIdleTimeoutSeconds int    `json:"session_idle_timeout_seconds,omitempty"`
	AgentHeartbeatSeconds     int    `json:"agent_heartbeat_seconds,omitempty"`
	ProvisionTimeoutSeconds   int    `json:"provision_timeout_seconds,omitempty"`
	CleanupTimeoutSeconds     int    `json:"cleanup_timeout_seconds,omitempty"`
	EnvironmentID             string `json:"environment_id,omitempty"`
	EnvironmentVersion        string `json:"environment_version,omitempty"`
}

func (w WindowsJobPoolConfig) Validate(pool JobPoolConfig) error {
	if !w.Enabled {
		if w != (WindowsJobPoolConfig{}) {
			return fmt.Errorf("%w: disabled windows_job_pool must be empty", ErrInvalidConfig)
		}
		return nil
	}
	if !pool.Enabled() || w.DesiredSlots != pool.DesiredSlots || w.EnvironmentID != pool.EnvironmentID || w.EnvironmentVersion != pool.EnvironmentVersion {
		return fmt.Errorf("%w: windows_job_pool must match the logical job_pool capacity and environment", ErrInvalidConfig)
	}
	if w.DesiredSlots < 0 || w.DesiredSlots > 256 || w.UserPrefix == "" || len(w.UserPrefix) > 12 || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,11}$`).MatchString(w.UserPrefix) {
		return fmt.Errorf("%w: invalid Windows job pool identity or capacity", ErrInvalidConfig)
	}
	if w.SessionIdleTimeoutSeconds < 0 || w.SessionIdleTimeoutSeconds > 604800 ||
		w.AgentHeartbeatSeconds < 1 || w.AgentHeartbeatSeconds > 300 ||
		w.ProvisionTimeoutSeconds < 1 || w.ProvisionTimeoutSeconds > 3600 ||
		w.CleanupTimeoutSeconds < 1 || w.CleanupTimeoutSeconds > 3600 {
		return fmt.Errorf("%w: Windows job pool timing is out of range", ErrInvalidConfig)
	}
	return nil
}

// JobPoolConfig describes the desired logical execution capacity. It contains
// only manifest/trust metadata; no Windows user or credential data is valid.
type JobPoolConfig struct {
	PoolID             string   `json:"pool_id,omitempty"`
	EnvironmentID      string   `json:"environment_id,omitempty"`
	EnvironmentVersion string   `json:"environment_version,omitempty"`
	DesiredSlots       int      `json:"desired_slots,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	ManifestDigest     string   `json:"manifest_digest,omitempty"`
	Signer             string   `json:"signer,omitempty"`
	RequireTrusted     bool     `json:"require_trusted,omitempty"`
}

func (p JobPoolConfig) Enabled() bool {
	return strings.TrimSpace(p.PoolID) != "" || strings.TrimSpace(p.EnvironmentID) != "" || strings.TrimSpace(p.EnvironmentVersion) != "" || p.DesiredSlots != 0 || len(p.Capabilities) != 0 || strings.TrimSpace(p.ManifestDigest) != "" || strings.TrimSpace(p.Signer) != "" || p.RequireTrusted
}

func (p JobPoolConfig) Validate() error {
	if !p.Enabled() {
		return nil
	}
	if strings.TrimSpace(p.PoolID) == "" || strings.TrimSpace(p.EnvironmentID) == "" || p.DesiredSlots < 0 || p.DesiredSlots > 10000 {
		return fmt.Errorf("%w: invalid job pool", ErrInvalidConfig)
	}
	if strings.TrimSpace(p.PoolID) != p.PoolID || strings.ContainsAny(p.PoolID, "\r\n\t ") || strings.TrimSpace(p.EnvironmentID) != p.EnvironmentID || strings.ContainsAny(p.EnvironmentID, "\r\n\t ") {
		return fmt.Errorf("%w: invalid job pool identifier", ErrInvalidConfig)
	}
	if len(p.EnvironmentVersion) > 128 || len(p.ManifestDigest) > 256 || len(p.Signer) > 256 || strings.ContainsAny(p.EnvironmentVersion, "\r\n") || strings.ContainsAny(p.ManifestDigest, "\r\n\t") || strings.ContainsAny(p.Signer, "\r\n\t") {
		return fmt.Errorf("%w: invalid job pool metadata", ErrInvalidConfig)
	}
	seen := make(map[string]struct{}, len(p.Capabilities))
	for _, capability := range p.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" || len(capability) > 128 || strings.ContainsAny(capability, "\r\n\t") {
			return fmt.Errorf("%w: invalid job pool capability", ErrInvalidConfig)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("%w: duplicate job pool capability", ErrInvalidConfig)
		}
		seen[capability] = struct{}{}
	}
	return nil
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

// RateLimitConfig configures sliding-window limits for new request
// submissions. A zero limit disables that dimension.
type RateLimitConfig struct {
	GlobalLimit          int `json:"global_limit,omitempty"`
	GlobalWindowSeconds  int `json:"global_window_seconds,omitempty"`
	ActorLimit           int `json:"actor_limit,omitempty"`
	ActorWindowSeconds   int `json:"actor_window_seconds,omitempty"`
	RoomLimit            int `json:"room_limit,omitempty"`
	RoomWindowSeconds    int `json:"room_window_seconds,omitempty"`
	AccountLimit         int `json:"account_limit,omitempty"`
	AccountWindowSeconds int `json:"account_window_seconds,omitempty"`
}

// CredentialConfig names deployment environment variables for the keyring.
// Values are names only; key material must remain outside ordinary config.
type CredentialConfig struct {
	KeyEnv     string `json:"key_env,omitempty"`
	KeyIDEnv   string `json:"key_id_env,omitempty"`
	HistoryEnv string `json:"history_env,omitempty"`
}

// HealthConfig controls the optional local health HTTP listener.
type HealthConfig struct {
	Listen string `json:"listen,omitempty"`
}

// New validates and normalizes a deployment data directory. Relative paths are
// resolved against the process working directory once, at configuration load.
func New(dataDir string) (Config, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" || strings.ContainsAny(dataDir, "\x00\r\n") || len(dataDir) > 4096 {
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
	normalized.Observability = raw.Observability
	normalized.Diagnostics = raw.Diagnostics
	normalized.RateLimit = raw.RateLimit
	normalized.EnvironmentPackage = raw.EnvironmentPackage
	normalized.JobPool = raw.JobPool
	normalized.WindowsJobPool = raw.WindowsJobPool
	if err := normalized.Validate(); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

// Validate checks a Config value, including values constructed as a struct
// literal rather than through New or Load.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" || !filepath.IsAbs(c.DataDir) || strings.ContainsAny(c.DataDir, "\x00\r\n") || len(c.DataDir) > 4096 {
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
	if err := c.Observability.Validate(); err != nil {
		return err
	}
	if err := c.Diagnostics.Validate(); err != nil {
		return err
	}
	if err := c.RateLimit.Validate(); err != nil {
		return err
	}
	if err := c.EnvironmentPackage.Validate(c.JobPool); err != nil {
		return err
	}
	if err := c.JobPool.Validate(); err != nil {
		return err
	}
	if err := c.WindowsJobPool.Validate(c.JobPool); err != nil {
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
	for _, name := range []string{strings.TrimSpace(c.KeyEnv), strings.TrimSpace(c.KeyIDEnv), strings.TrimSpace(c.HistoryEnv)} {
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

// ObservabilityConfig controls optional local diagnostics. It contains no
// credentials; log files are created owner-only by the observability package.
type ObservabilityConfig struct {
	MetricsListen string `json:"metrics_listen,omitempty"`
	LogPath       string `json:"log_path,omitempty"`
	LogMaxBytes   int64  `json:"log_max_bytes,omitempty"`
	LogMaxFiles   int    `json:"log_max_files,omitempty"`
}

// DiagnosticsConfig controls the optional, explicit support-report queue.
// Endpoint is ordinary configuration; reports never carry credentials.
type DiagnosticsConfig struct {
	Enabled          bool   `json:"enabled,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	QueueDir         string `json:"queue_dir,omitempty"`
	MaxReportBytes   int64  `json:"max_report_bytes,omitempty"`
	MaxQueueFiles    int    `json:"max_queue_files,omitempty"`
	RetryBaseSeconds int    `json:"retry_base_seconds,omitempty"`
	RetryMaxSeconds  int    `json:"retry_max_seconds,omitempty"`
}

func (d DiagnosticsConfig) Validate() error {
	if strings.TrimSpace(d.Endpoint) != "" {
		u, err := url.Parse(strings.TrimSpace(d.Endpoint))
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
			return fmt.Errorf("%w: diagnostics endpoint is invalid", ErrInvalidConfig)
		}
		if u.Scheme != "https" {
			host := strings.ToLower(u.Hostname())
			if u.Scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
				return fmt.Errorf("%w: diagnostics endpoint must use HTTPS", ErrInvalidConfig)
			}
		}
	}
	if strings.TrimSpace(d.QueueDir) != d.QueueDir || strings.ContainsAny(d.QueueDir, "\r\n") || len(d.QueueDir) > 1024 {
		return fmt.Errorf("%w: diagnostics queue_dir is invalid", ErrInvalidConfig)
	}
	if d.MaxReportBytes < 0 || d.MaxReportBytes > 1<<20 || d.MaxQueueFiles < 0 || d.MaxQueueFiles > 1000 || d.RetryBaseSeconds < 0 || d.RetryBaseSeconds > 86400 || d.RetryMaxSeconds < 0 || d.RetryMaxSeconds > 604800 {
		return fmt.Errorf("%w: diagnostics limits are out of range", ErrInvalidConfig)
	}
	return nil
}

func (o ObservabilityConfig) Validate() error {
	if strings.TrimSpace(o.MetricsListen) != "" {
		if _, _, err := net.SplitHostPort(strings.TrimSpace(o.MetricsListen)); err != nil {
			return fmt.Errorf("%w: observability metrics_listen must be host:port", ErrInvalidConfig)
		}
	}
	if strings.TrimSpace(o.LogPath) != o.LogPath || strings.ContainsAny(o.LogPath, "\r\n") || len(o.LogPath) > 1024 {
		return fmt.Errorf("%w: observability log_path is invalid", ErrInvalidConfig)
	}
	if o.LogMaxBytes < 0 || o.LogMaxBytes > 1<<40 || o.LogMaxFiles < 0 || o.LogMaxFiles > 100 {
		return fmt.Errorf("%w: observability log rotation limits are out of range", ErrInvalidConfig)
	}
	return nil
}

func (r RateLimitConfig) Validate() error {
	for _, value := range []int{r.GlobalLimit, r.ActorLimit, r.RoomLimit, r.AccountLimit} {
		if value < 0 || value > 1_000_000 {
			return fmt.Errorf("%w: rate limit count is out of range", ErrInvalidConfig)
		}
	}
	for _, value := range []int{r.GlobalWindowSeconds, r.ActorWindowSeconds, r.RoomWindowSeconds, r.AccountWindowSeconds} {
		if value < 0 || value > 86400 {
			return fmt.Errorf("%w: rate limit window is out of range", ErrInvalidConfig)
		}
	}
	limits := []int{r.GlobalLimit, r.ActorLimit, r.RoomLimit, r.AccountLimit}
	windows := []int{r.GlobalWindowSeconds, r.ActorWindowSeconds, r.RoomWindowSeconds, r.AccountWindowSeconds}
	for index, limit := range limits {
		if limit > 0 && windows[index] == 0 {
			return fmt.Errorf("%w: rate limit window is required", ErrInvalidConfig)
		}
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
