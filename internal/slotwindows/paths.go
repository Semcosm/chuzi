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
	"strconv"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
)

var (
	ErrInvalidOptions        = errors.New("slotwindows: invalid options")
	ErrUnsupported           = errors.New("slotwindows: Windows runtime required")
	ErrHostPreflight         = errors.New("slotwindows: host preflight failed")
	ErrIdentityCollision     = errors.New("slotwindows: managed username identity collision")
	slotIDPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	userPrefixPattern        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,11}$`)
	managedGenerationPattern = regexp.MustCompile(`^generation-[0-9]{6}$`)
	profileDirectoryPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	managedUsernamePattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{1,19}$`)
)

const managedDesktopPrefix = "ChuziSlot"

// ManagedIdentityAlgorithm is part of the service/broker identity contract.
// The scope is the canonical configured DataDir for one single-node
// installation; the slot ID and ordinal are service-owned values.
const ManagedIdentityAlgorithm = "sha256-scope-slot-v2"

const managedUsernameHashLength = 8 // 32 bits, checked against ownership records.

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
	// Runtime is the manager-validated package handoff. The legacy path
	// fields below remain for native smoke fixtures; service assembly always
	// supplies Runtime.
	Runtime     environment.RuntimeHandoff
	RuntimePath string
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
	RuntimeResolver func(context.Context, slot.EnvironmentRequirement) (environment.RuntimeHandoff, error)
	// SessionBootstrapper is intentionally optional. When nil, Provision keeps
	// the existing external-session prerequisite and fails closed if FindSession
	// cannot locate the managed SID.
	SessionBootstrapper SessionBootstrapper
	// HostPreflight validates fixed machine requirements after the managed SID
	// has been verified and before its Profile or WTS session is touched.
	HostPreflight HostPreflight
	// SessionLoginAdapter is the deployment-owned credential boundary for
	// adapters that establish a real WTS session using a short-lived password
	// buffer. It is never serialized onto the broker protocol.
	SessionLoginAdapter SessionLoginAdapter
}

type HostPreflight interface {
	Check(context.Context, string) error
}

type Paths struct {
	Root           string
	Generation     string
	Work           string
	Temp           string
	Logs           string
	Metadata       string
	CleanupJournal string
	Secret         string
	UserName       string
	Desktop        string
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
	deferredRuntime := o.RuntimeResolver != nil && o.EnvironmentID == "" && o.Version == "" && o.ManifestDigest == "" && o.Signer == "" && !o.Runtime.Valid()
	if !filepath.IsAbs(o.DataDir) || filepath.Clean(o.DataDir) == string(filepath.Separator) || containsUnsafePathText(o.DataDir) || !userPrefixPattern.MatchString(o.UserPrefix) || (!deferredRuntime && o.EnvironmentID == "") || len(o.EnvironmentID) > 256 || len(o.Version) > 128 || len(o.ManifestDigest) > 256 || len(o.Signer) > 256 || strings.ContainsAny(o.EnvironmentID+o.Version+o.ManifestDigest+o.Signer, "\x00\r\n\t") {
		return ErrInvalidOptions
	}
	for _, value := range []string{o.AgentPath, o.RuntimePath, o.WorkerRuntimeRoot, o.WorkerCommand, o.WorkerScript, o.AdapterScript, o.BrowserCommand} {
		if filepath.Clean(value) == string(filepath.Separator) || containsUnsafePathText(value) {
			return ErrInvalidOptions
		}
	}
	if o.Runtime.Valid() && (o.RuntimePath != o.Runtime.PackageRoot() || o.WorkerScript != o.Runtime.WorkerPath() || o.AdapterScript != o.Runtime.AdapterBridgePath() || o.EnvironmentID != o.Runtime.EnvironmentID() || o.Version != o.Runtime.Version() || o.ManifestDigest != o.Runtime.ManifestDigest() || o.Signer != o.Runtime.Signer()) {
		return ErrInvalidOptions
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
// caller-provided account/profile value can influence this layout. The
// username is stable across environment generations and unique within an
// installation when the ownership collision fence is honoured.
func (o Options) DerivePaths(slotID string, ordinal int, generation uint64) (Paths, error) {
	if err := o.Validate(); err != nil || !slotIDPattern.MatchString(slotID) || slotID == "." || slotID == ".." || ordinal < 1 || ordinal > 256 || generation == 0 {
		return Paths{}, ErrInvalidOptions
	}
	root := filepath.Join(filepath.Clean(o.DataDir), "job-slots", slotID)
	gen := filepath.Join(root, fmt.Sprintf("generation-%06d", generation))
	user, err := o.DeriveUsername(slotID, ordinal)
	if err != nil {
		return Paths{}, err
	}
	desktop, err := managedDesktopName(slotID)
	if err != nil {
		return Paths{}, ErrInvalidOptions
	}
	return Paths{Root: root, Generation: gen, Work: filepath.Join(gen, "work"), Temp: filepath.Join(gen, "tmp"), Logs: filepath.Join(gen, "logs"), Metadata: filepath.Join(root, "ownership.json"), CleanupJournal: filepath.Join(filepath.Clean(o.DataDir), "cleanup-journal", slotID+".json"), Secret: filepath.Join(root, "account.dpapi"), UserName: user, Desktop: desktop}, nil
}

// DeriveUsername returns the only username that the service may use for a
// slot. DataDir is the installation scope; changing it is an explicit
// installation migration and therefore intentionally derives a different
// host identity. The short hash is not treated as a probabilistic uniqueness
// proof: Windows provisioning checks all durable ownership records and fails
// closed on a collision before touching the OS account.
func (o Options) DeriveUsername(slotID string, ordinal int) (string, error) {
	if err := o.Validate(); err != nil || !slotIDPattern.MatchString(slotID) || slotID == "." || slotID == ".." || ordinal < 1 || ordinal > 256 {
		return "", ErrInvalidOptions
	}
	scope := installationScope(o.DataDir)
	preimage := ManagedIdentityAlgorithm + "\x00" + scope + "\x00" + slotID + "\x00" + strconv.Itoa(ordinal)
	digest := sha256.Sum256([]byte(preimage))
	username := o.UserPrefix + hex.EncodeToString(digest[:])[:managedUsernameHashLength]
	if err := validateManagedUsername(username); err != nil {
		return "", err
	}
	return username, nil
}

// LegacyUsername is retained only for the Windows provisioner migration fence.
// It is never accepted as a new identity without matching ownership metadata
// and the recorded SID.
func (o Options) LegacyUsername(ordinal int) (string, error) {
	if err := o.Validate(); err != nil || ordinal < 1 || ordinal > 256 {
		return "", ErrInvalidOptions
	}
	username := fmt.Sprintf("%s%04d", o.UserPrefix, ordinal)
	if err := validateManagedUsername(username); err != nil {
		return "", err
	}
	return username, nil
}

func installationScope(dataDir string) string {
	clean := filepath.Clean(dataDir)
	if absolute, err := filepath.Abs(clean); err == nil {
		clean = absolute
	}
	// Resolve existing junctions/symlinks so two configuration spellings of
	// one installation cannot allocate two independent identity scopes.
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	return strings.ToLower(filepath.ToSlash(clean))
}

func validateManagedUsername(username string) error {
	if len(username) < 2 || len(username) > 20 || !managedUsernamePattern.MatchString(username) {
		return ErrInvalidOptions
	}
	return nil
}

// identityClaimCollides is deliberately exact: a truncated hash may only be
// allocated to one slot claim in an installation. The Windows implementation
// supplies claims read from ownership.json before creating an OS user.
func identityClaimCollides(candidateUsername, candidateSlotID string, candidateOrdinal int, existingUsername, existingSlotID string, existingOrdinal int) bool {
	return candidateUsername == existingUsername && (candidateSlotID != existingSlotID || candidateOrdinal != existingOrdinal)
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
