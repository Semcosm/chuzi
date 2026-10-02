// Package slotwindows contains the Windows job-slot boundary. Path derivation
// is platform-neutral so the same validation is exercised on every host.
package slotwindows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
)

var (
	ErrInvalidOptions        = errors.New("slotwindows: invalid options")
	ErrUnsupported           = errors.New("slotwindows: Windows runtime required")
	slotIDPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	userPrefixPattern        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,11}$`)
	managedGenerationPattern = regexp.MustCompile(`^generation-[0-9]{6}$`)
	profileDirectoryPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const managedDesktopPrefix = "ChuziSlot"

type Options struct {
	DataDir        string
	UserPrefix     string
	EnvironmentID  string
	Version        string
	ManifestDigest string
	Signer         string
	RequireTrusted bool
	RDPEnabled     bool
	AgentPath      string
	RuntimePath    string
	// WorkerRuntimeRoot is the service-owned host runtime directory (for
	// example the directory containing node.exe). The signed environment
	// remains RuntimePath and supplies the worker script/resources.
	WorkerRuntimeRoot  string
	WorkerCommand      string
	WorkerScript       string
	AdapterScript      string
	BrowserMode        string
	BrowserCommand     string
	SessionIdleTimeout time.Duration
	CapabilityRevoker  CapabilityRevoker
	// RuntimeResolver revalidates the signed package selected by a durable pool
	// update. It returns only service-owned runtime paths; control-plane input
	// cannot supply an executable or filesystem path.
	RuntimeResolver func(context.Context, slot.EnvironmentRequirement) (string, string, error)
	// SessionBootstrapper is intentionally optional. When nil, Provision keeps
	// the existing external-session prerequisite and fails closed if FindSession
	// cannot locate the managed SID.
	SessionBootstrapper SessionBootstrapper
}

type Paths struct {
	Root       string
	Generation string
	Work       string
	Temp       string
	Logs       string
	Metadata   string
	Secret     string
	UserName   string
	Desktop    string
}

type Provisioner interface {
	slot.EnvironmentProvisioner
	ProfileAccess
}

// ProfileAccess grants one active slot access to one service-derived account
// Profile. Implementations must revoke the ACL after the worker stops.
type ProfileAccess interface {
	GrantProfile(context.Context, string, string) error
	RevokeProfile(context.Context, string, string) error
}

// CapabilityRevoker is intentionally slot-scoped. The provisioner can revoke
// all interactive capabilities before deleting a managed Windows identity.
type CapabilityRevoker interface {
	RevokeSlot(context.Context, string) error
}

func (o Options) Validate() error {
	if !filepath.IsAbs(o.DataDir) || filepath.Clean(o.DataDir) == string(filepath.Separator) || containsUnsafePathText(o.DataDir) || !userPrefixPattern.MatchString(o.UserPrefix) || o.EnvironmentID == "" || len(o.EnvironmentID) > 256 || len(o.Version) > 128 || len(o.ManifestDigest) > 256 || len(o.Signer) > 256 || strings.ContainsAny(o.EnvironmentID+o.Version+o.ManifestDigest+o.Signer, "\x00\r\n\t") {
		return ErrInvalidOptions
	}
	for _, value := range []string{o.AgentPath, o.RuntimePath, o.WorkerRuntimeRoot, o.WorkerCommand, o.WorkerScript, o.AdapterScript, o.BrowserCommand} {
		if filepath.Clean(value) == string(filepath.Separator) || containsUnsafePathText(value) {
			return ErrInvalidOptions
		}
	}
	if o.SessionIdleTimeout < 0 {
		return ErrInvalidOptions
	}
	if o.BrowserMode != "" && o.BrowserMode != "headless" && o.BrowserMode != "headed" {
		return ErrInvalidOptions
	}
	if o.BrowserMode != "" && strings.TrimSpace(o.BrowserCommand) == "" {
		return ErrInvalidOptions
	}
	if o.BrowserMode == "" && o.BrowserCommand != "" {
		return ErrInvalidOptions
	}
	return nil
}

func containsUnsafePathText(value string) bool {
	return value != "" && (len(value) > 512 || strings.ContainsAny(value, "\x00\r\n"))
}

// DerivePaths accepts only service-owned slot identity. No request path or
// caller-provided account/profile value can influence this layout.
func (o Options) DerivePaths(slotID string, ordinal int, generation uint64) (Paths, error) {
	if err := o.Validate(); err != nil || !slotIDPattern.MatchString(slotID) || slotID == "." || slotID == ".." || ordinal < 1 || ordinal > 256 || generation == 0 {
		return Paths{}, ErrInvalidOptions
	}
	root := filepath.Join(filepath.Clean(o.DataDir), "job-slots", slotID)
	gen := filepath.Join(root, fmt.Sprintf("generation-%06d", generation))
	user := fmt.Sprintf("%s%04d", o.UserPrefix, ordinal)
	desktop, err := managedDesktopName(slotID)
	if err != nil {
		return Paths{}, ErrInvalidOptions
	}
	return Paths{Root: root, Generation: gen, Work: filepath.Join(gen, "work"), Temp: filepath.Join(gen, "tmp"), Logs: filepath.Join(gen, "logs"), Metadata: filepath.Join(root, "ownership.json"), Secret: filepath.Join(root, "account.dpapi"), UserName: user, Desktop: desktop}, nil
}

// managedDesktopName keeps the Win32 desktop opaque and bounded while making
// it deterministic for exactly one service-owned slot identity.
func managedDesktopName(slotID string) (string, error) {
	if !slotIDPattern.MatchString(slotID) {
		return "", ErrInvalidOptions
	}
	digest := sha256.Sum256([]byte(slotID))
	return managedDesktopPrefix + hex.EncodeToString(digest[:8]), nil
}
