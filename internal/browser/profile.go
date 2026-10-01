// Package browser contains the session-runner boundary between the durable
// queue and an isolated browser worker. It does not implement browser
// automation or decide account business state.
package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Semcosm/chuzi/internal/config"
)

const profileDirectory = "profiles"

var (
	ErrInvalidProfile = errors.New("browser: invalid profile")
	ErrProfileBusy    = errors.New("browser: profile is already in use")
)

// Profiles derives one private, deterministic directory per account. Account
// IDs are hashed before becoming path components, so request input can never
// introduce separators, dot segments, or an arbitrary filesystem path.
type Profiles struct {
	root   string
	mu     sync.Mutex
	active map[string]struct{}
}

// NewProfiles derives the profile root from the deployment data directory.
func NewProfiles(cfg config.Config) (*Profiles, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	root := filepath.Join(cfg.DataDir, profileDirectory)
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: profile root", ErrInvalidProfile)
	}
	return &Profiles{root: root, active: make(map[string]struct{})}, nil
}

// Root returns the service-derived profile root. Callers cannot replace it
// with a request-provided path.
func (p *Profiles) Root() string {
	if p == nil {
		return ""
	}
	return p.root
}

// Path returns the stable directory for an account without creating it.
func (p *Profiles) Path(accountID string) (string, error) {
	if p == nil || strings.TrimSpace(p.root) == "" || strings.TrimSpace(accountID) == "" {
		return "", ErrInvalidProfile
	}
	digest := sha256.Sum256([]byte(accountID))
	name := hex.EncodeToString(digest[:])
	path := filepath.Join(p.root, name)
	relative, err := filepath.Rel(p.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", ErrInvalidProfile
	}
	return path, nil
}

// Prepare creates an account profile directory with owner-only permissions.
// Profiles are intentionally retained between attempts so a later session
// can reuse the authorized browser state; cleanup is an explicit policy layer.
func (p *Profiles) Prepare(accountID string) (string, error) {
	path, err := p.Path(accountID)
	if err != nil {
		return "", err
	}
	if err := p.validateTarget(path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("create browser profile: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", fmt.Errorf("restrict browser profile: %w", err)
	}
	return path, nil
}

// Acquire reserves an account profile for one in-process worker. Durable
// account leases still provide the cross-process guard; this lock prevents a
// caller from accidentally starting two workers before the store notices.
func (p *Profiles) Acquire(accountID string) (string, func(), error) {
	path, err := p.Path(accountID)
	if err != nil {
		return "", nil, err
	}
	p.mu.Lock()
	if _, exists := p.active[accountID]; exists {
		p.mu.Unlock()
		return "", nil, ErrProfileBusy
	}
	p.active[accountID] = struct{}{}
	p.mu.Unlock()
	path, err = p.Prepare(accountID)
	if err != nil {
		p.release(accountID)
		return "", nil, err
	}
	return path, func() { p.release(accountID) }, nil
}

// Remove purges the service-derived profile for an account. It is idempotent
// when the directory is already absent and rejects symlinked targets.
func (p *Profiles) Remove(accountID string) error {
	path, err := p.Path(accountID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.active[accountID]; exists {
		return ErrProfileBusy
	}
	if err := p.validateTarget(path); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove browser profile: %w", err)
	}
	return nil
}

// Retain validates the service-derived profile without changing it. The
// deletion aggregate records the retain decision; this boundary only ensures
// a caller cannot turn it into an arbitrary filesystem path.
func (p *Profiles) Retain(accountID string) error {
	path, err := p.Path(accountID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.active[accountID]; exists {
		return ErrProfileBusy
	}
	return p.validateTarget(path)
}

func (p *Profiles) validateTarget(path string) error {
	if p == nil || path == "" {
		return ErrInvalidProfile
	}
	rootInfo, err := os.Lstat(p.root)
	if err == nil && rootInfo.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidProfile
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect browser profile root: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect browser profile: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidProfile
	}
	relative, err := filepath.Rel(p.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) || relative == "." {
		return ErrInvalidProfile
	}
	return nil
}

func (p *Profiles) release(accountID string) {
	p.mu.Lock()
	delete(p.active, accountID)
	p.mu.Unlock()
}
