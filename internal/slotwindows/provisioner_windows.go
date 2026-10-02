//go:build windows

package slotwindows

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotagent"
	"golang.org/x/sys/windows"
)

var (
	ErrOwnership            = errors.New("slotwindows: managed user ownership check failed")
	ErrACLDrift             = errors.New("slotwindows: managed ACL check failed")
	ErrCleanup              = errors.New("slotwindows: managed resource cleanup failed")
	ErrCleanupAgent         = errors.New("slotwindows: agent cleanup failed")
	ErrCleanupSession       = errors.New("slotwindows: session cleanup failed")
	ErrCleanupRoot          = errors.New("slotwindows: root cleanup failed")
	ErrCleanupUser          = errors.New("slotwindows: user cleanup failed")
	ErrCleanupUserInspect   = errors.New("slotwindows: user cleanup inspection failed")
	ErrCleanupUserOwnership = errors.New("slotwindows: user cleanup ownership mismatch")
	ErrCleanupUserDelete    = errors.New("slotwindows: user deletion failed")
	ErrCleanupUserMarker    = errors.New("slotwindows: user cleanup marker mismatch")
	ErrCleanupUserPrivilege = errors.New("slotwindows: user cleanup privilege mismatch")
	ErrCleanupUserDisabled  = errors.New("slotwindows: user cleanup disabled")
	ErrCleanupUserAdmin     = errors.New("slotwindows: user cleanup administrator membership")
	ErrCleanupUserSID       = errors.New("slotwindows: user cleanup SID mismatch")
	ErrSessionGroupLookup   = errors.New("slotwindows: remote desktop group lookup failed")
	ErrSessionGroupAdd      = errors.New("slotwindows: remote desktop group add failed")
	ErrSessionGroupVerify   = errors.New("slotwindows: remote desktop group membership failed")
	ErrSessionPolicy        = errors.New("slotwindows: remote interactive policy failed")
	ErrSessionUserLookup    = errors.New("slotwindows: managed user SID lookup failed")
	ErrManagedUserSID       = errors.New("slotwindows: managed user SID unavailable")
)

type retryableProvisionFailure struct{ cause error }

func (e retryableProvisionFailure) Error() string   { return e.cause.Error() }
func (e retryableProvisionFailure) Unwrap() error   { return e.cause }
func (e retryableProvisionFailure) Retryable() bool { return true }

type retryableSessionFailure struct{ cause error }

func (e retryableSessionFailure) Error() string   { return e.cause.Error() }
func (e retryableSessionFailure) Unwrap() error   { return e.cause }
func (e retryableSessionFailure) Retryable() bool { return true }

const (
	userPrivUser                   = 1
	userFlagScript                 = 0x0001
	userFlagNormalAccount          = 0x0200
	userFlagDontExpirePassword     = 0x10000
	userFlagDisabled               = 0x0002
	netErrorUserExists             = 2224
	netErrorUserNotFound           = 2221
	netErrorMemberInAlias          = 1378
	localGroupIncludeIndirect      = 1
	fileAttributeDirectory         = 0x10
	fileAttributeReparsePoint      = 0x400
	seDaclSecurityInformation      = 0x00000004
	seFileObject                   = 1
	grantAccess                    = 1
	denyAccess                     = 3
	revokeAccess                   = 4
	trusteeIsSid                   = 0
	trusteeIsUser                  = 1
	subContainersAndObjectsInherit = 3
	fileModifyMask                 = 0x001301bf
	fileReadExecuteMask            = 0x001200a9
	fileTraverseMask               = 0x00120020
)

type userInfo1 struct {
	Name        *uint16
	Password    *uint16
	PasswordAge uint32
	Priv        uint32
	HomeDir     *uint16
	Comment     *uint16
	Flags       uint32
	ScriptPath  *uint16
}

type userSnapshot struct {
	comment     string
	priv, flags uint32
}
type localGroupMember0 struct{ SID *windows.SID }
type localGroupUsers0 struct{ Name *uint16 }
type ownershipRecord struct {
	Version    int
	SlotID     string
	Ordinal    int
	SID        string
	Generation uint64
}
type profileGrantRecord struct {
	Version int
	Profile string
}

type windowsProvisioner struct {
	options Options
	mu      sync.Mutex
	agents  map[string]agentProcess
	boots   map[string]sessionBootstrapState
}

type agentProcess struct {
	process           *Process
	token             string
	pipe              string
	leaseID           string
	owner             string
	generation        uint64
	sid               string
	session           uint32
	disconnectedSince time.Time
}

type sessionBootstrapState struct {
	identity ManagedIdentity
	session  BootstrapSession
}

type managedUserCredentials struct {
	SID      string
	password []uint16
}

func (c *managedUserCredentials) clear() {
	if c == nil {
		return
	}
	clear(c.password)
	c.password = nil
}

// LeaseHealthChecker is an optional lifecycle hook used while a slot lease is
// active. It receives the authoritative lease so the probe cannot accidentally
// use the provisioning fence after a job has been prepared.
func (p *windowsProvisioner) Health(ctx context.Context, request slot.ProvisionRequest, lease slot.Lease) error {
	if err := p.validateRequest(request); err != nil || lease.Validate() != nil || lease.SlotID != request.SlotID || lease.EnvironmentGeneration != request.EnvironmentGeneration {
		return ErrOwnership
	}
	if lease.Expired(time.Now().UTC()) {
		// Never treat an expired durable lease as a healthy agent. The scheduler
		// owns recovery, but this fence prevents a concurrent lifecycle pass from
		// extending or reusing the stale session.
		return slot.ErrLeaseExpired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	agent, ok := p.agents[request.SlotID]
	p.mu.Unlock()
	if !ok || agent.process == nil {
		return retryableSessionFailure{cause: ErrSessionUnavailable}
	}
	if _, err := VerifySession(agent.sid, agent.session); err != nil {
		if !errors.Is(err, ErrSessionDisconnected) {
			if errors.Is(err, ErrSessionChanged) {
				// The captured session ID is no longer owned by this managed
				// identity. Fence the old agent before the lease can be reused;
				// leaving it alive would let a stale desktop retain access.
				if cleanupErr := p.terminateAgent(request.SlotID, agent); cleanupErr != nil {
					return cleanupErr
				}
			}
			if isRetryableSessionError(err) {
				return retryableSessionFailure{cause: err}
			}
			return err
		}
	}
	request.Owner = lease.Owner
	response, err := p.agentHealthWithLease(ctx, agent, request, lease.LeaseID, lease.RequestID)
	if err != nil {
		// A reconcile tick can race the worker's prepare_slot command. If the
		// agent still accepts its bootstrap fence, leave the durable lease for
		// the runner to finish preparing instead of quarantining a healthy slot.
		if errors.Is(err, slotagent.ErrStaleLease) {
			if bootstrapErr := p.agentHealth(ctx, agent, request); bootstrapErr == nil {
				return nil
			}
		}
		p.markAgentSession(request.SlotID, "unavailable")
		if cleanupErr := p.terminateAgent(request.SlotID, agent); cleanupErr != nil {
			return cleanupErr
		}
		if isRetryableSessionError(err) {
			return retryableSessionFailure{cause: err}
		}
		return err
	}
	state := response.SessionState
	if state == "disconnected" {
		p.mu.Lock()
		current := p.agents[request.SlotID]
		if current.disconnectedSince.IsZero() {
			current.disconnectedSince = time.Now().UTC()
			p.agents[request.SlotID] = current
		}
		disconnectedSince := current.disconnectedSince
		p.mu.Unlock()
		if p.options.SessionIdleTimeout > 0 && time.Since(disconnectedSince) >= p.options.SessionIdleTimeout {
			if cleanupErr := p.terminateAgent(request.SlotID, agent); cleanupErr != nil {
				return cleanupErr
			}
			return retryableSessionFailure{cause: ErrSessionUnavailable}
		}
		return retryableSessionFailure{cause: ErrSessionDisconnected}
	}
	p.markAgentSession(request.SlotID, state)
	return nil
}

func (p *windowsProvisioner) markAgentSession(slotID, state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	agent, ok := p.agents[slotID]
	if !ok {
		return
	}
	if state == "disconnected" {
		if agent.disconnectedSince.IsZero() {
			agent.disconnectedSince = time.Now().UTC()
		}
	} else {
		agent.disconnectedSince = time.Time{}
	}
	p.agents[slotID] = agent
}

// AgentEndpoint returns only the service-owned pipe and opaque lease token;
// callers cannot obtain the Windows username, SID, password, or profile path.
func (p *windowsProvisioner) AgentEndpoint(slotID, handle string) (string, string, error) {
	if p == nil || handle != "slot:"+slotID {
		return "", "", ErrOwnership
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	agent, ok := p.agents[slotID]
	if !ok || agent.pipe == "" || agent.token == "" {
		return "", "", ErrSessionUnavailable
	}
	return agent.pipe, agent.token, nil
}

// StopSlotLease fences a job before the scheduler removes an expired durable
// lease. A stale response is success: the agent's protocol server disconnects
// all jobs when it observes the same stale lease fence.
func (p *windowsProvisioner) StopSlotLease(ctx context.Context, lease slot.Lease) error {
	if p == nil || ctx == nil {
		return ErrOwnership
	}
	if err := lease.Validate(); err != nil {
		return ErrOwnership
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	agent, ok := p.agents[lease.SlotID]
	p.mu.Unlock()
	if !ok || agent.process == nil || agent.generation != lease.EnvironmentGeneration {
		return nil
	}
	if agent.pipe == "" || agent.token == "" {
		return p.terminateAgent(lease.SlotID, agent)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client, err := slotagent.Dial(stopCtx, agent.pipe)
	if err == nil {
		_, err = client.Call(stopCtx, slotagent.Request{
			CommandID:             fmt.Sprintf("stop-expired-%d", time.Now().UnixNano()),
			RequestID:             lease.RequestID,
			Owner:                 lease.Owner,
			SlotID:                lease.SlotID,
			LeaseID:               lease.LeaseID,
			EnvironmentGeneration: lease.EnvironmentGeneration,
			Auth:                  agent.token,
			Command:               slotagent.StopJob,
		})
		_ = client.Close()
		if err == nil || errors.Is(err, slotagent.ErrStaleLease) {
			return nil
		}
	}
	return p.terminateAgent(lease.SlotID, agent)
}

func (p *windowsProvisioner) terminateAgent(slotID string, agent agentProcess) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminateAgentLocked(slotID, agent)
}

func (p *windowsProvisioner) terminateAgentWithRetry(ctx context.Context, slotID string, agent agentProcess) error {
	if ctx == nil {
		return ErrCleanup
	}
	for attempt := 0; attempt < 6; attempt++ {
		if err := p.terminateAgent(slotID, agent); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ErrCleanup
		}
		select {
		case <-ctx.Done():
			return ErrCleanup
		case <-time.After(time.Duration(100*(1<<attempt)) * time.Millisecond):
		}
		p.mu.Lock()
		current, ok := p.agents[slotID]
		p.mu.Unlock()
		if !ok {
			return nil
		}
		agent = current
	}
	return ErrCleanup
}

// terminateAgentLocked closes a mapped agent while the agent registry is
// held. Keeping the map check and process fence in one critical section avoids
// a replacement agent being removed by a stale cleanup path.
func (p *windowsProvisioner) terminateAgentLocked(slotID string, agent agentProcess) error {
	if agent.process == nil {
		if current, ok := p.agents[slotID]; ok && current.token == agent.token {
			delete(p.agents, slotID)
		}
		return nil
	}
	terminateErr := agent.process.Terminate()
	closeErr := agent.process.Close()
	if terminateErr != nil || closeErr != nil {
		return ErrCleanup
	}
	if current, ok := p.agents[slotID]; ok && current.token == agent.token {
		delete(p.agents, slotID)
	}
	return nil
}

// Shutdown closes all service-owned agent trees. Durable slots remain
// provisioned so the next service instance can inspect and recreate agents.
func (p *windowsProvisioner) Shutdown(ctx context.Context) error {
	if p == nil || ctx == nil {
		return ErrCleanup
	}
	p.mu.Lock()
	agents := make(map[string]agentProcess, len(p.agents))
	for slotID, agent := range p.agents {
		agents[slotID] = agent
	}
	p.mu.Unlock()
	var cleanupErr error
	for slotID, agent := range agents {
		if ctx.Err() != nil {
			cleanupErr = errors.Join(cleanupErr, ErrCleanup)
			break
		}
		if p.options.CapabilityRevoker != nil {
			if err := p.options.CapabilityRevoker.RevokeSlot(ctx, slotID); err != nil {
				cleanupErr = errors.Join(cleanupErr, ErrCleanup)
			}
		}
		if err := p.terminateAgentWithRetry(ctx, slotID, agent); err != nil {
			cleanupErr = errors.Join(cleanupErr, ErrCleanup)
		}
		if err := p.stopSessionBootstrap(ctx, slotID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if cleanupErr != nil {
		return ErrCleanup
	}
	return nil
}

func New(options Options) (Provisioner, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &windowsProvisioner{options: options, agents: make(map[string]agentProcess), boots: make(map[string]sessionBootstrapState)}, nil
}

func (p *windowsProvisioner) Provision(ctx context.Context, request slot.ProvisionRequest) (result slot.ProvisionResult, provisionErr error) {
	if err := p.validateRequest(request); err != nil {
		return slot.ProvisionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return slot.ProvisionResult{}, err
	}
	paths, err := p.options.DerivePaths(request.SlotID, request.Ordinal, request.EnvironmentGeneration)
	if err != nil {
		return slot.ProvisionResult{}, err
	}
	userExisted := false
	if _, lookupErr := lookupSID(paths.UserName); lookupErr == nil {
		userExisted = true
	} else if !errors.Is(lookupErr, windows.ERROR_NONE_MAPPED) {
		return slot.ProvisionResult{}, ErrOwnership
	}
	rootExisted, err := managedPathExists(paths.Root)
	if err != nil {
		return slot.ProvisionResult{}, ErrOwnership
	}
	// Only resources absent at the start of this attempt are eligible for
	// rollback. Existing managed resources may belong to a running generation.
	cleanupOnFailure := true
	var sid string
	var managed managedUserCredentials
	defer managed.clear()
	defer func() {
		if provisionErr == nil || !cleanupOnFailure {
			return
		}
		if cleanupErr := p.rollbackProvision(paths, request, sid, !userExisted, !rootExisted); cleanupErr != nil {
			result = slot.ProvisionResult{}
			// Preserve the original stable Provision failure alongside cleanup
			// diagnostics. Cleanup must never hide a failed session, ACL, or
			// process prerequisite from the native smoke report.
			provisionErr = errors.Join(provisionErr, ErrCleanup, cleanupErr)
		}
	}()
	managed, err = ensureManagedUser(paths, request)
	if err != nil {
		return slot.ProvisionResult{}, err
	}
	sid = managed.SID
	if p.options.RDPEnabled {
		if err := ensureRemoteDesktopMembership(paths.UserName); err != nil {
			return slot.ProvisionResult{}, err
		}
		// Group membership alone is insufficient: local policy may deny remote
		// interactive logon or omit the effective allow right.
		if err := verifyRemoteDesktopMembership(paths.UserName); err != nil {
			return slot.ProvisionResult{}, err
		}
	}
	if err := ensureSlotDirectories(paths, sid); err != nil {
		return slot.ProvisionResult{}, ErrACLDrift
	}
	if err := p.ensureSessionBootstrap(ctx, request, paths.UserName, managed); err != nil {
		return slot.ProvisionResult{}, err
	}
	if err := p.ensureAgent(ctx, paths, request, sid); err != nil {
		return slot.ProvisionResult{}, err
	}
	if err := p.verifyProfileGrant(paths, sid); err != nil {
		return slot.ProvisionResult{}, err
	}
	cleanupOnFailure = false
	return slot.ProvisionResult{AgentHandle: "slot:" + request.SlotID, Summary: p.summary(request, sid)}, nil
}

func (p *windowsProvisioner) rollbackProvision(paths Paths, request slot.ProvisionRequest, sid string, removeUser, removeRoot bool) error {
	var cleanupErr error
	p.mu.Lock()
	agent, present := p.agents[request.SlotID]
	p.mu.Unlock()
	if present {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := p.terminateAgentWithRetry(cleanupCtx, request.SlotID, agent)
		cancel()
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, ErrCleanupAgent)
		}
	}
	{
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := p.stopSessionBootstrap(cleanupCtx, request.SlotID); err != nil {
			cleanupErr = errors.Join(cleanupErr, ErrCleanupSession)
		}
		// The runner-owned broker may have returned before Winlogon released the
		// profile token. Re-issue the SID-scoped logoff fence before deleting the
		// disposable identity; NetUserDel otherwise commonly reports a transient
		// busy/profile-in-use failure even though FindSession has disappeared.
		if sid != "" {
			if err := logoffManagedSessions(cleanupCtx, sid); err != nil {
				cleanupErr = errors.Join(cleanupErr, ErrCleanupSession)
			}
		}
		cancel()
	}
	if removeRoot {
		if err := removeOwnedTree(paths.Root); err != nil {
			cleanupErr = errors.Join(cleanupErr, ErrCleanupRoot)
		}
	}
	if removeUser {
		if sid == "" {
			if actual, err := lookupSID(paths.UserName); err == nil {
				sid = actual
			} else if errors.Is(err, windows.ERROR_NONE_MAPPED) {
				return cleanupErr
			} else {
				cleanupErr = errors.Join(cleanupErr, ErrCleanup)
			}
		}
		if sid != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := deleteManagedProfileWithRetry(cleanupCtx, paths.UserName, sid); err != nil {
				cleanupErr = errors.Join(cleanupErr, ErrCleanupUser, err)
			}
			if err := deleteManagedUserWithRetry(cleanupCtx, paths.UserName, request.SlotID, request.Ordinal, sid); err != nil {
				cleanupErr = errors.Join(cleanupErr, ErrCleanupUser, err)
			}
			cancel()
		}
	}
	return cleanupErr
}

func (p *windowsProvisioner) Inspect(ctx context.Context, request slot.ProvisionRequest) (slot.EnvironmentSummary, error) {
	if err := p.validateRequest(request); err != nil {
		return slot.EnvironmentSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return slot.EnvironmentSummary{}, err
	}
	paths, err := p.options.DerivePaths(request.SlotID, request.Ordinal, request.EnvironmentGeneration)
	if err != nil {
		return slot.EnvironmentSummary{}, err
	}
	record, err := readOwnership(paths.Metadata)
	if err != nil || record.SlotID != request.SlotID || record.Ordinal != request.Ordinal {
		return slot.EnvironmentSummary{}, ErrOwnership
	}
	sid, err := validateManagedUser(paths, request, record.SID)
	if err != nil {
		return slot.EnvironmentSummary{}, err
	}
	if err := inspectSlotDirectories(paths, sid); err != nil {
		return slot.EnvironmentSummary{}, ErrACLDrift
	}
	if p.options.RDPEnabled {
		if err := verifyRemoteDesktopMembership(paths.UserName); err != nil {
			return slot.EnvironmentSummary{}, ErrSessionIdentity
		}
	}
	if err := p.verifyProfileGrant(paths, sid); err != nil {
		return slot.EnvironmentSummary{}, err
	}
	if err := p.ensureAgent(ctx, paths, request, sid); err != nil {
		return slot.EnvironmentSummary{}, err
	}
	return p.summary(request, sid), nil
}

func (p *windowsProvisioner) Retire(ctx context.Context, request slot.ProvisionRequest) error {
	if ctx == nil {
		return ErrCleanup
	}
	if err := p.validateRequest(request); err != nil {
		return err
	}
	if p.options.CapabilityRevoker != nil {
		// Capability revocation is the deletion fence. If it cannot be
		// confirmed, preserve the managed identity and its profile ACLs for a
		// later retry rather than deleting the owner while access may remain.
		if err := p.options.CapabilityRevoker.RevokeSlot(ctx, request.SlotID); err != nil {
			return ErrCleanup
		}
	}
	paths, err := p.options.DerivePaths(request.SlotID, request.Ordinal, request.EnvironmentGeneration)
	if err != nil {
		return err
	}
	record, err := readOwnership(paths.Metadata)
	if errors.Is(err, fs.ErrNotExist) {
		rootExists, rootErr := managedPathExists(paths.Root)
		if rootErr != nil {
			return ErrOwnership
		}
		_, userErr := lookupSID(paths.UserName)
		if rootExists || userErr == nil {
			return ErrOwnership
		}
		if !errors.Is(userErr, windows.ERROR_NONE_MAPPED) {
			return ErrOwnership
		}
		return nil
	}
	if err != nil || record.SlotID != request.SlotID || record.Ordinal != request.Ordinal {
		return ErrOwnership
	}
	if _, err := validateManagedUser(paths, request, record.SID); err != nil {
		return err
	}
	var agentCleanupErr error
	p.mu.Lock()
	if agent, ok := p.agents[request.SlotID]; ok {
		if agent.process == nil {
			agentCleanupErr = ErrCleanup
		} else {
			shutdownErr := p.shutdownAgent(ctx, agent, request)
			terminateErr := agent.process.Terminate()
			closeErr := agent.process.Close()
			if shutdownErr != nil || terminateErr != nil || closeErr != nil {
				agentCleanupErr = ErrCleanup
			} else {
				delete(p.agents, request.SlotID)
			}
		}
	}
	p.mu.Unlock()
	if agentCleanupErr != nil {
		return agentCleanupErr
	}
	if err := p.stopSessionBootstrap(ctx, request.SlotID); err != nil {
		return err
	}
	if err := p.revokeRecordedProfile(paths, record.SID); err != nil {
		return ErrCleanup
	}
	dataDir := filepath.Dir(filepath.Dir(paths.Root))
	if err := revokeControlPlaneFiles(dataDir, record.SID); err != nil {
		return ErrCleanup
	}
	if err := logoffManagedSessions(ctx, record.SID); err != nil {
		return ErrCleanup
	}
	if err := deleteManagedProfileWithRetry(ctx, paths.UserName, record.SID); err != nil {
		return ErrCleanup
	}
	if err := removeOwnedTree(paths.Root); err != nil {
		return ErrCleanup
	}
	if err := deleteManagedUserWithRetry(ctx, paths.UserName, request.SlotID, request.Ordinal, record.SID); err != nil {
		return ErrCleanup
	}
	return nil
}

func (p *windowsProvisioner) GrantProfile(ctx context.Context, slotID, profilePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, record, err := p.ownedSlot(slotID)
	if err != nil {
		return err
	}
	profile, err := p.validateProfilePath(profilePath)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	grantPath := filepath.Join(paths.Root, "profile-access.json")
	if current, readErr := readProfileGrant(grantPath); readErr == nil {
		if filepath.Clean(current.Profile) != profile {
			return ErrACLDrift
		}
		return nil
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return ErrACLDrift
	}
	if err := changeProfileACL(profile, record.SID, true); err != nil {
		return ErrACLDrift
	}
	if err := verifyProfileACL(profile, record.SID); err != nil {
		_ = changeProfileACL(profile, record.SID, false)
		return ErrACLDrift
	}
	data, err := json.Marshal(profileGrantRecord{Version: 1, Profile: profile})
	if err != nil {
		_ = changeProfileACL(profile, record.SID, false)
		return ErrACLDrift
	}
	if err := writeServiceFile(grantPath, data); err != nil {
		_ = changeProfileACL(profile, record.SID, false)
		return ErrACLDrift
	}
	return nil
}

func (p *windowsProvisioner) RevokeProfile(ctx context.Context, slotID, profilePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, record, err := p.ownedSlot(slotID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	grantPath := filepath.Join(paths.Root, "profile-access.json")
	grant, err := readProfileGrant(grantPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrACLDrift
	}
	profile, err := p.validateProfilePath(profilePath)
	if err != nil || filepath.Clean(grant.Profile) != profile {
		return ErrACLDrift
	}
	if err := changeProfileACL(profile, record.SID, false); err != nil {
		return ErrACLDrift
	}
	if err := verifyProfileACLAbsent(profile, record.SID); err != nil {
		return ErrACLDrift
	}
	if err := os.Remove(grantPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrACLDrift
	}
	return nil
}

func (p *windowsProvisioner) ownedSlot(slotID string) (Paths, ownershipRecord, error) {
	if !slotIDPattern.MatchString(slotID) {
		return Paths{}, ownershipRecord{}, ErrOwnership
	}
	root := filepath.Join(filepath.Clean(p.options.DataDir), "job-slots", slotID)
	metadata := filepath.Join(root, "ownership.json")
	record, err := readOwnership(metadata)
	if err != nil || record.SlotID != slotID {
		return Paths{}, ownershipRecord{}, ErrOwnership
	}
	return Paths{Root: root, Metadata: metadata}, record, nil
}

func (p *windowsProvisioner) validateProfilePath(value string) (string, error) {
	if !filepath.IsAbs(value) || strings.IndexByte(value, 0) >= 0 {
		return "", ErrACLDrift
	}
	root := filepath.Join(filepath.Clean(p.options.DataDir), "profiles")
	profile := filepath.Clean(value)
	relative, err := filepath.Rel(strings.ToLower(root), strings.ToLower(profile))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) || !strings.EqualFold(filepath.Dir(profile), root) || !profileDirectoryPattern.MatchString(strings.ToLower(filepath.Base(profile))) {
		return "", ErrACLDrift
	}
	if err := inspectDirectoryNoReparseChain(profile); err != nil {
		return "", ErrACLDrift
	}
	return profile, nil
}

func (p *windowsProvisioner) verifyProfileGrant(paths Paths, sid string) error {
	grant, err := readProfileGrant(filepath.Join(paths.Root, "profile-access.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrACLDrift
	}
	profile, err := p.validateProfilePath(grant.Profile)
	if err != nil {
		return err
	}
	return verifyProfileACL(profile, sid)
}

func (p *windowsProvisioner) revokeRecordedProfile(paths Paths, sid string) error {
	grant, err := readProfileGrant(filepath.Join(paths.Root, "profile-access.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrACLDrift
	}
	profile, err := p.validateProfilePath(grant.Profile)
	if err != nil {
		return err
	}
	if err := changeProfileACL(profile, sid, false); err != nil {
		return err
	}
	if err := verifyProfileACLAbsent(profile, sid); err != nil {
		return err
	}
	return nil
}

func (p *windowsProvisioner) summary(request slot.ProvisionRequest, sid string) slot.EnvironmentSummary {
	// The worker and agent are fixed service-packaged components; their
	// executable and ACL allowlists are verified before this summary is
	// returned, so the resulting local environment satisfies trusted-pool
	// selection without exposing the managed SID.
	digest, signer := request.Requirement.ManifestDigest, request.Requirement.Signer
	if p.options.ManifestDigest != "" {
		digest = p.options.ManifestDigest
	}
	if p.options.Signer != "" {
		signer = p.options.Signer
	}
	return slot.EnvironmentSummary{EnvironmentID: p.options.EnvironmentID, Version: p.options.Version, Generation: request.EnvironmentGeneration, Capabilities: append([]string(nil), request.Requirement.Capabilities...), ManifestDigest: digest, Signer: signer, Trusted: true, AgentVersion: "chuzi-user-agent/v1", AgentHandle: "slot:" + request.SlotID, SessionState: "ready", DesktopReady: true, UpdatedAt: time.Now().UTC()}
}

func (p *windowsProvisioner) managedIdentity(request slot.ProvisionRequest, sid string) (ManagedIdentity, error) {
	identity := ManagedIdentity{SlotID: request.SlotID, Ordinal: request.Ordinal, Generation: request.EnvironmentGeneration, SID: sid}
	if err := identity.validate(); err != nil {
		return ManagedIdentity{}, err
	}
	return identity, nil
}

// ensureSessionBootstrap invokes only the optional, deployment-owned session
// provider. It always re-queries WTS after Start; a provider result alone can
// never satisfy the production FindSession fence.
func (p *windowsProvisioner) ensureSessionBootstrap(ctx context.Context, request slot.ProvisionRequest, username string, managed managedUserCredentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := p.managedIdentity(request, managed.SID)
	if err != nil {
		return err
	}
	if current, findErr := FindSession(managed.SID); findErr == nil && current.State == "active" {
		return nil
	}
	provider := p.options.SessionBootstrapper
	if provider == nil {
		return retryableProvisionFailure{cause: ErrSessionUnavailable}
	}

	p.mu.Lock()
	previous, hadPrevious := p.boots[request.SlotID]
	p.mu.Unlock()
	if hadPrevious {
		if current, findErr := FindSession(managed.SID); findErr == nil && current.ID == previous.session.ID && current.State == "active" {
			return nil
		}
		_ = p.stopSessionBootstrap(ctx, request.SlotID)
	}
	started, startErr := startManagedSession(ctx, provider, identity, username, managed.password)
	if startErr != nil {
		return retryableProvisionFailure{cause: classifySessionBootstrapError(startErr)}
	}
	if started.ID == 0 || started.State != "active" {
		if stopErr := provider.Stop(ctx, identity, started); stopErr != nil {
			return ErrCleanup
		}
		return retryableProvisionFailure{cause: ErrSessionUnavailable}
	}
	current, findErr := FindSession(managed.SID)
	if findErr != nil {
		if stopErr := provider.Stop(ctx, identity, started); stopErr != nil {
			return ErrCleanup
		}
		return retryableProvisionFailure{cause: classifySessionBootstrapError(findErr)}
	}
	if current.ID != started.ID || current.State != "active" {
		if stopErr := provider.Stop(ctx, identity, started); stopErr != nil {
			return ErrCleanup
		}
		return retryableProvisionFailure{cause: ErrSessionChanged}
	}
	p.mu.Lock()
	p.boots[request.SlotID] = sessionBootstrapState{identity: identity, session: started}
	p.mu.Unlock()
	return nil
}

func classifySessionBootstrapError(err error) error {
	switch {
	case errors.Is(err, ErrSessionChanged):
		return ErrSessionChanged
	case errors.Is(err, ErrSessionDisconnected):
		return ErrSessionDisconnected
	case errors.Is(err, ErrSessionIdentity):
		return ErrSessionIdentity
	case errors.Is(err, ErrSessionUnavailable), errors.Is(err, ErrSessionBootstrapUnavailable):
		return ErrSessionUnavailable
	default:
		// Provider implementation errors are intentionally collapsed to the
		// stable session classification; raw Win32/provider text must not cross
		// the smoke, log, or control-plane boundary.
		return ErrSessionUnavailable
	}
}

func (p *windowsProvisioner) stopSessionBootstrap(ctx context.Context, slotID string) error {
	if p == nil || p.options.SessionBootstrapper == nil {
		return nil
	}
	p.mu.Lock()
	state, ok := p.boots[slotID]
	p.mu.Unlock()
	if !ok {
		return nil
	}
	if err := p.options.SessionBootstrapper.Stop(ctx, state.identity, state.session); err != nil {
		return ErrCleanup
	}
	if err := waitForSessionGone(ctx, state.identity.SID); err != nil {
		return ErrCleanup
	}
	p.mu.Lock()
	if current, present := p.boots[slotID]; present && current.identity == state.identity && current.session == state.session {
		delete(p.boots, slotID)
	}
	p.mu.Unlock()
	return nil
}

func waitForSessionGone(ctx context.Context, sid string) error {
	if sid == "" {
		return ErrSessionIdentity
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := FindSession(sid)
		if errors.Is(err, ErrSessionUnavailable) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *windowsProvisioner) ensureAgent(ctx context.Context, paths Paths, request slot.ProvisionRequest, sid string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.options.AgentPath == "" || !filepath.IsAbs(p.options.AgentPath) {
		return ErrSessionUnavailable
	}
	if err := validateRuntimePaths(p.options); err != nil {
		return err
	}
	for _, runtimeFile := range []string{p.options.AgentPath, p.options.WorkerCommand, p.options.WorkerScript, p.options.AdapterScript} {
		if runtimeFile != "" {
			if err := verifyRuntimeACL(runtimeFile, sid); err != nil {
				return err
			}
		}
	}
	session, err := FindSession(sid)
	if err != nil {
		if errors.Is(err, ErrSessionDisconnected) || errors.Is(err, ErrSessionUnavailable) {
			p.mu.Lock()
			current, present := p.agents[request.SlotID]
			var cleanupErr error
			if present {
				cleanupErr = p.terminateAgentLocked(request.SlotID, current)
			}
			p.mu.Unlock()
			if cleanupErr != nil {
				return cleanupErr
			}
			return retryableProvisionFailure{cause: err}
		}
		return err
	}
	if err := ensureSessionDesktop(sid, session.ID, paths.Desktop); err != nil {
		if isRetryableSessionError(err) {
			return retryableProvisionFailure{cause: err}
		}
		return err
	}
	if err := verifyRuntimeACL(p.options.RuntimePath, sid); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.agents[request.SlotID]; ok {
		if current.process == nil {
			return ErrCleanup
		}
		if _, waitErr := current.process.Wait(0); errors.Is(waitErr, context.DeadlineExceeded) {
			if current.sid == sid && current.session == session.ID && p.agentHealth(ctx, current, request) == nil {
				if verifySessionDesktop(sid, session.ID, paths.Desktop) == nil {
					return nil
				}
			}
		}
		if terminateErr := current.process.Terminate(); terminateErr != nil {
			return terminateErr
		}
		if closeErr := current.process.Close(); closeErr != nil {
			return closeErr
		}
		delete(p.agents, request.SlotID)
	}
	token, err := randomPasswordUTF16(48)
	if err != nil {
		return ErrSessionIdentity
	}
	tokenText := make([]byte, len(token)-1)
	for i, value := range token[:len(token)-1] {
		tokenText[i] = byte(value)
	}
	clear(token)
	tokenValue := string(tokenText)
	pipe := "\\\\.\\pipe\\chuzi-slot-" + request.SlotID
	serviceSID, sidErr := currentProcessSID()
	if sidErr != nil {
		return ErrSessionIdentity
	}
	environment := map[string]string{
		"CHUZI_AGENT_SLOT_ID":             request.SlotID,
		"CHUZI_AGENT_PIPE":                pipe,
		"CHUZI_AGENT_LEASE_ID":            "provision-" + request.SlotID,
		"CHUZI_AGENT_TOKEN":               tokenValue,
		"CHUZI_AGENT_GENERATION":          fmt.Sprintf("%d", request.EnvironmentGeneration),
		"CHUZI_AGENT_REQUEST_ID":          "maintenance-" + request.SlotID,
		"CHUZI_AGENT_ACCOUNT_ID":          "maintenance-" + request.SlotID,
		"CHUZI_AGENT_OWNER":               request.Owner,
		"CHUZI_AGENT_VERSION":             "chuzi-user-agent/v1",
		"CHUZI_AGENT_SESSION_STATE":       "ready",
		"CHUZI_AGENT_RUNTIME_ROOT":        p.options.RuntimePath,
		"CHUZI_AGENT_WORKER_RUNTIME_ROOT": p.options.WorkerRuntimeRoot,
		"CHUZI_AGENT_PROFILE_ROOT":        filepath.Join(filepath.Clean(p.options.DataDir), "profiles"),
		"CHUZI_AGENT_WORK_DIR":            paths.Work,
		"CHUZI_AGENT_WORKER_COMMAND":      p.options.WorkerCommand,
		"CHUZI_AGENT_WORKER_SCRIPT":       p.options.WorkerScript,
		"CHUZI_AGENT_ADAPTER_SCRIPT":      p.options.AdapterScript,
		"CHUZI_AGENT_BROWSER_MODE":        p.options.BrowserMode,
		"CHUZI_AGENT_BROWSER_COMMAND":     p.options.BrowserCommand,
		"CHUZI_AGENT_PIPE_SERVICE_SID":    serviceSID,
	}
	clear(tokenText)
	process, err := StartProcessAsSlotUserForSlotID(sid, session.ID, request.SlotID, p.options.AgentPath, []string{p.options.AgentPath}, environment, paths.Work)
	if err != nil {
		return err
	}
	agent := agentProcess{process: process, token: tokenValue, pipe: pipe, leaseID: "provision-" + request.SlotID, owner: request.Owner, generation: request.EnvironmentGeneration, sid: sid, session: session.ID}
	p.agents[request.SlotID] = agent
	if err := p.agentHealth(ctx, agent, request); err != nil {
		terminateErr := process.Terminate()
		closeErr := process.Close()
		delete(p.agents, request.SlotID)
		if terminateErr != nil || closeErr != nil {
			return ErrCleanup
		}
		if isRetryableSessionError(err) {
			return retryableProvisionFailure{cause: err}
		}
		return ErrSessionUnavailable
	}
	return nil
}

func validateRuntimePaths(options Options) error {
	root := filepath.Clean(options.RuntimePath)
	if !filepath.IsAbs(root) || inspectDirectoryNoReparseChain(root) != nil {
		return ErrACLDrift
	}
	if !filepath.IsAbs(options.AgentPath) || !strings.EqualFold(filepath.Base(options.AgentPath), "chuzi-user-agent.exe") || !runtimeRegularNoReparse(options.AgentPath) {
		return ErrACLDrift
	}
	if !runtimeFileUnderRoot(root, options.WorkerScript) {
		return ErrACLDrift
	}
	workerRoot := filepath.Clean(options.WorkerRuntimeRoot)
	if !filepath.IsAbs(workerRoot) || inspectDirectoryNoReparseChain(workerRoot) != nil || !runtimeFileUnderRoot(workerRoot, options.WorkerCommand) {
		return ErrACLDrift
	}
	if options.AdapterScript != "" && !runtimeFileUnderRoot(root, options.AdapterScript) {
		return ErrACLDrift
	}
	if !strings.EqualFold(filepath.Base(options.WorkerCommand), "node.exe") {
		return ErrACLDrift
	}
	for _, script := range []string{options.WorkerScript, options.AdapterScript} {
		if script == "" {
			continue
		}
		rel, err := filepath.Rel(root, filepath.Clean(script))
		if err != nil || (rel != filepath.Join("browser-worker", "src", "worker.mjs") && rel != filepath.Join("browser-worker", "src", "headless.mjs") && rel != filepath.Join("browser-worker", "src", "headless-adapter.mjs")) {
			return ErrACLDrift
		}
	}
	return nil
}

func runtimeRegularNoReparse(path string) bool {
	if inspectNoReparseChain(path) != nil {
		return false
	}
	attrs, err := fileAttributes(filepath.Clean(path))
	return err == nil && attrs&fileAttributeDirectory == 0 && attrs&fileAttributeReparsePoint == 0
}

func runtimeFileUnderRoot(root, path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(clean))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	if err := inspectNoReparseChain(clean); err != nil {
		return false
	}
	attrs, err := fileAttributes(clean)
	return err == nil && attrs&fileAttributeDirectory == 0
}

func isRetryableSessionError(err error) bool {
	return errors.Is(err, ErrSessionUnavailable) || errors.Is(err, ErrSessionDisconnected) || errors.Is(err, ErrSessionChanged)
}

func verifyRuntimeACL(path, sid string) error {
	if !filepath.IsAbs(path) || strings.TrimSpace(path) == "" {
		return ErrACLDrift
	}
	if err := inspectNoReparseChain(path); err != nil {
		return ErrACLDrift
	}
	attrs, err := fileAttributes(filepath.Clean(path))
	if err != nil || attrs&fileAttributeReparsePoint != 0 {
		return ErrACLDrift
	}
	security, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	// The managed SID must not have an explicit ACE on the installation tree;
	// the deployment ACL is responsible for read/execute access instead.
	if !runtimeACLAllowsReadExecute(security.String(), sid) {
		return ErrACLDrift
	}
	return nil
}

// runtimeACLAllowsReadExecute accepts the inherited ACL shapes produced by
// common Windows installers. A file can carry both an inherited and an
// explicit BUILTIN\Users read/execute ACE, so requiring one exact ACE would
// reject a safe deployment. Every BUILTIN\Users ACE must remain read/execute
// only, and at least one must grant the complete read/execute mask.
func runtimeACLAllowsReadExecute(sddl, managedSID string) bool {
	seenUsers := false
	complete := false
	for start := strings.IndexByte(sddl, '('); start >= 0; {
		end := strings.IndexByte(sddl[start+1:], ')')
		if end < 0 {
			return false
		}
		end += start + 1
		fields := strings.Split(sddl[start+1:end], ";")
		if len(fields) == 6 {
			account := fields[5]
			if strings.EqualFold(account, managedSID) {
				return false
			}
			if account == "BU" || strings.EqualFold(account, "S-1-5-32-545") {
				seenUsers = true
				if fields[0] != "A" {
					return false
				}
				mask, ok := runtimeACLMask(fields[2])
				if !ok || mask&^uint32(fileReadExecuteMask) != 0 {
					return false
				}
				if mask&uint32(fileReadExecuteMask) == uint32(fileReadExecuteMask) {
					complete = true
				}
			} else if broadRuntimeAccount(account) {
				// A broad write or deny ACE can override the slot user's
				// effective access through group membership. Fail closed even
				// when a separate BUILTIN Users ACE looks safe.
				mask, ok := runtimeACLMask(fields[2])
				if fields[0] != "A" || !ok || mask&^uint32(fileReadExecuteMask) != 0 {
					return false
				}
			}
		}
		next := end + 1
		if next >= len(sddl) {
			break
		}
		remaining := sddl[next:]
		index := strings.IndexByte(remaining, '(')
		if index < 0 {
			break
		}
		start = next + index
	}
	return seenUsers && complete
}

func broadRuntimeAccount(account string) bool {
	switch strings.ToUpper(account) {
	case "WD", "AU", "AN", "NU", "IU", "SU", "S-1-1-0", "S-1-5-11":
		return true
	default:
		return false
	}
}

func runtimeACLMask(rights string) (uint32, bool) {
	if strings.HasPrefix(rights, "0x") || strings.HasPrefix(rights, "0X") {
		value, err := strconv.ParseUint(rights[2:], 16, 32)
		return uint32(value), err == nil
	}
	// SDDL's RX and GRGX aliases are the symbolic forms of the same
	// directory read/execute permission used by the packaged runtime.
	switch strings.ToUpper(rights) {
	case "RX", "GRGX":
		return uint32(fileReadExecuteMask), true
	default:
		return 0, false
	}
}

func (p *windowsProvisioner) agentHealth(ctx context.Context, agent agentProcess, request slot.ProvisionRequest) error {
	response, err := p.agentHealthWithLease(ctx, agent, request, agent.leaseID, "maintenance-"+request.SlotID)
	if err == nil && response.SessionState == "disconnected" {
		return ErrSessionDisconnected
	}
	return err
}

func (p *windowsProvisioner) agentHealthWithLease(ctx context.Context, agent agentProcess, request slot.ProvisionRequest, leaseID, requestID string) (slotagent.Response, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client, err := slotagent.Dial(checkCtx, agent.pipe)
	if err != nil {
		return slotagent.Response{}, ErrSessionUnavailable
	}
	defer client.Close()
	response, err := client.Call(checkCtx, slotagent.Request{CommandID: fmt.Sprintf("health-%d", time.Now().UnixNano()), RequestID: requestID, Owner: request.Owner, SlotID: request.SlotID, LeaseID: leaseID, EnvironmentGeneration: request.EnvironmentGeneration, Auth: agent.token, Command: slotagent.Health})
	if err == nil && response.SessionState != "disconnected" {
		if _, verifyErr := VerifySession(agent.sid, agent.session); verifyErr != nil {
			if isRetryableSessionError(verifyErr) {
				return slotagent.Response{}, retryableSessionFailure{cause: verifyErr}
			}
			return slotagent.Response{}, verifyErr
		}
	}
	if err != nil {
		return slotagent.Response{}, err
	}
	return response, nil
}

func (p *windowsProvisioner) shutdownAgent(ctx context.Context, agent agentProcess, request slot.ProvisionRequest) error {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client, err := slotagent.Dial(checkCtx, agent.pipe)
	if err != nil {
		return err
	}
	defer client.Close()
	_, err = client.Call(checkCtx, slotagent.Request{CommandID: fmt.Sprintf("shutdown-%d", time.Now().UnixNano()), RequestID: "maintenance-" + request.SlotID, Owner: agent.owner, SlotID: request.SlotID, LeaseID: agent.leaseID, EnvironmentGeneration: request.EnvironmentGeneration, Auth: agent.token, Command: slotagent.Shutdown})
	return err
}

func validateProvisionRequest(request slot.ProvisionRequest) error {
	if request.SlotID == "" || request.PoolID == "" || request.Ordinal < 1 || strings.TrimSpace(request.Owner) != request.Owner || request.Owner == "" || len(request.Owner) > 160 || strings.ContainsAny(request.Owner, "\x00\r\n\t/\\") || request.EnvironmentGeneration == 0 || request.Requirement.EnvironmentID == "" {
		return ErrInvalidOptions
	}
	if _, err := request.Requirement.Normalize(); err != nil {
		return ErrInvalidOptions
	}
	return nil
}

func (p *windowsProvisioner) validateRequest(request slot.ProvisionRequest) error {
	if p == nil {
		return ErrInvalidOptions
	}
	if err := validateProvisionRequest(request); err != nil {
		return err
	}
	requirement, err := request.Requirement.Normalize()
	if err != nil || requirement.EnvironmentID != p.options.EnvironmentID || requirement.Version != p.options.Version || (p.options.ManifestDigest != "" && requirement.ManifestDigest != p.options.ManifestDigest) || (p.options.Signer != "" && requirement.Signer != p.options.Signer) || (p.options.RequireTrusted && !requirement.RequireTrusted) {
		return ErrInvalidOptions
	}
	return nil
}

func ensureManagedUser(paths Paths, request slot.ProvisionRequest) (managedUserCredentials, error) {
	marker := fmt.Sprintf("CHUZI-MANAGED:%s:%d", request.SlotID, request.Ordinal)
	if sid, err := lookupSID(paths.UserName); err == nil {
		info, infoErr := getUserInfo(paths.UserName)
		if infoErr != nil || info.comment != marker || info.priv != userPrivUser || info.flags&userFlagDisabled != 0 || hasAdministratorsMembership(paths.UserName) {
			return managedUserCredentials{}, ErrOwnership
		}
		record, recordErr := readOwnership(paths.Metadata)
		if errors.Is(recordErr, fs.ErrNotExist) {
			// A matching comment is not proof of service ownership: it is
			// forgeable and the SID may have drifted after deletion/recreation.
			// Require the durable ownership record and fail closed on its loss.
			return managedUserCredentials{}, ErrOwnership
		}
		if recordErr != nil || record.SlotID != request.SlotID || record.Ordinal != request.Ordinal || record.SID != sid {
			return managedUserCredentials{}, ErrOwnership
		}
		if record.Generation > request.EnvironmentGeneration {
			// A stale provision request must not move ownership metadata back to
			// an older environment generation.
			return managedUserCredentials{}, ErrOwnership
		}
		if record.Generation < request.EnvironmentGeneration {
			record.Generation = request.EnvironmentGeneration
			if err := replaceOwnership(paths.Metadata, record); err != nil {
				return managedUserCredentials{}, err
			}
		}
		return managedUserCredentials{SID: sid}, nil
	} else if !errors.Is(err, windows.ERROR_NONE_MAPPED) {
		return managedUserCredentials{}, ErrOwnership
	}
	_, recordErr := readOwnership(paths.Metadata)
	if recordErr == nil {
		// Recreating a missing user would assign a new SID. Keep the old
		// ownership record as a drift fence instead of adopting that identity.
		return managedUserCredentials{}, ErrOwnership
	} else if !errors.Is(recordErr, fs.ErrNotExist) {
		return managedUserCredentials{}, ErrOwnership
	} else {
		rootExists, rootErr := managedPathExists(paths.Root)
		if rootErr != nil || rootExists {
			return managedUserCredentials{}, ErrOwnership
		}
	}
	password, err := randomPasswordUTF16(48)
	if err != nil {
		return managedUserCredentials{}, ErrSessionIdentity
	}
	keepPassword := false
	defer func() {
		if !keepPassword {
			clear(password)
		}
	}()
	name, err := windows.UTF16PtrFromString(paths.UserName)
	if err != nil {
		return managedUserCredentials{}, ErrInvalidOptions
	}
	comment, err := windows.UTF16PtrFromString(marker)
	if err != nil {
		return managedUserCredentials{}, ErrInvalidOptions
	}
	info := userInfo1{Name: name, Password: &password[0], Priv: userPrivUser, Comment: comment, Flags: userFlagScript | userFlagNormalAccount | userFlagDontExpirePassword}
	status := callNetUserAdd(&info)
	if status == netErrorUserExists {
		// Another reconcile may have created the same managed identity between
		// lookup and NetUserAdd. Re-read and validate it instead of treating an
		// idempotent race as an ownership failure.
		sid, lookupErr := lookupSID(paths.UserName)
		if lookupErr != nil {
			return managedUserCredentials{}, ErrOwnership
		}
		infoSnapshot, infoErr := getUserInfo(paths.UserName)
		if infoErr != nil || infoSnapshot.comment != marker || infoSnapshot.priv != userPrivUser || infoSnapshot.flags&userFlagDisabled != 0 || hasAdministratorsMembership(paths.UserName) {
			return managedUserCredentials{}, ErrOwnership
		}
		record, recordErr := readOwnership(paths.Metadata)
		if recordErr == nil {
			if record.SlotID != request.SlotID || record.Ordinal != request.Ordinal || record.SID != sid || record.Generation > request.EnvironmentGeneration {
				return managedUserCredentials{}, ErrOwnership
			}
			if record.Generation < request.EnvironmentGeneration {
				record.Generation = request.EnvironmentGeneration
				if err := replaceOwnership(paths.Metadata, record); err != nil {
					return managedUserCredentials{}, err
				}
			}
			return managedUserCredentials{SID: sid}, nil
		}
		// NetUserAdd can race another provision attempt, but a missing or
		// unreadable ownership record still cannot establish which SID owns the
		// slot. Do not adopt the user or rewrite metadata in that case.
		return managedUserCredentials{}, ErrOwnership
	}
	if status != 0 {
		return managedUserCredentials{}, ErrOwnership
	}
	sid, err := lookupSIDEventually(paths.UserName)
	if err != nil {
		return managedUserCredentials{}, ErrManagedUserSID
	}
	if recordErr == nil {
		// Recreating a missing user would produce a new SID. Preserve the old
		// record as a drift fence instead of silently adopting that identity.
		return managedUserCredentials{}, ErrOwnership
	} else if !errors.Is(recordErr, fs.ErrNotExist) {
		return managedUserCredentials{}, ErrOwnership
	} else if err := writeOwnership(paths.Metadata, ownershipRecord{Version: 1, SlotID: request.SlotID, Ordinal: request.Ordinal, SID: sid, Generation: request.EnvironmentGeneration}); err != nil {
		_ = deleteManagedUser(paths.UserName, request.SlotID, request.Ordinal, sid)
		return managedUserCredentials{}, ErrOwnership
	}
	keepPassword = true
	return managedUserCredentials{SID: sid, password: password}, nil
}

func replaceOwnership(path string, record ownershipRecord) error {
	if err := inspectNoReparseChain(path); err != nil {
		return ErrOwnership
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ErrOwnership
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ownership-*")
	if err != nil {
		return ErrOwnership
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return ErrOwnership
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrOwnership
	}
	if err := temporary.Close(); err != nil {
		return ErrOwnership
	}
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return ErrOwnership
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrOwnership
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return ErrOwnership
	}
	removeTemporary = false
	return nil
}

func managedPathExists(path string) (bool, error) {
	if err := inspectNoReparseChain(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	attrs, err := fileAttributes(path)
	if err == nil {
		if attrs&fileAttributeReparsePoint != 0 {
			return false, ErrOwnership
		}
		return true, nil
	}
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return false, nil
	}
	return false, err
}

func validateManagedUser(paths Paths, request slot.ProvisionRequest, expectedSID string) (string, error) {
	info, err := getUserInfo(paths.UserName)
	if err != nil || info.comment != fmt.Sprintf("CHUZI-MANAGED:%s:%d", request.SlotID, request.Ordinal) || info.priv != userPrivUser || info.flags&userFlagDisabled != 0 {
		return "", ErrOwnership
	}
	sid, err := lookupSID(paths.UserName)
	if err != nil || (expectedSID != "" && sid != expectedSID) {
		return "", ErrOwnership
	}
	if hasAdministratorsMembership(paths.UserName) {
		return "", ErrOwnership
	}
	record, readErr := readOwnership(paths.Metadata)
	if readErr == nil {
		if record.SlotID != request.SlotID || record.Ordinal != request.Ordinal || record.SID != sid || record.Generation != request.EnvironmentGeneration {
			return "", ErrOwnership
		}
	} else {
		return "", ErrOwnership
	}
	return sid, nil
}

func ensureSlotDirectories(paths Paths, sid string) error {
	serviceSID, err := currentProcessSID()
	if err != nil {
		return ErrACLDrift
	}
	for _, path := range []string{paths.Root, paths.Generation, paths.Work, paths.Temp, paths.Logs} {
		if err := ensureNoReparseDirectory(path); err != nil {
			return err
		}
	}
	if err := setDirectoryACL(paths.Root, sid, serviceSID, "root"); err != nil {
		return err
	}
	if err := setDirectoryACL(paths.Generation, sid, serviceSID, "generation"); err != nil {
		return err
	}
	for _, path := range []string{paths.Work, paths.Temp, paths.Logs} {
		if err := setDirectoryACL(path, sid, serviceSID, "private"); err != nil {
			return err
		}
	}
	// Create the backup directory before applying its inherited deny ACE. This
	// keeps backups created after provisioning inside the same control-plane
	// protection boundary.
	dataDir := filepath.Dir(filepath.Dir(paths.Root))
	if err := ensureNoReparseDirectory(filepath.Join(dataDir, "backups")); err != nil {
		return ErrACLDrift
	}
	if err := protectControlPlaneFiles(dataDir, sid); err != nil {
		return err
	}
	for path, scope := range map[string]string{paths.Root: "root", paths.Generation: "generation", paths.Work: "private", paths.Temp: "private", paths.Logs: "private"} {
		if err := verifyDirectoryACL(path, sid, serviceSID, scope); err != nil {
			return err
		}
	}
	if err := verifyControlPlaneFiles(dataDir, sid); err != nil {
		return err
	}
	return nil
}

// protectControlPlaneFiles adds an explicit per-slot deny to shared durable
// state. The slot user still receives access only to its own derived tree;
// service and administrator entries remain intact.
func protectControlPlaneFiles(dataDir, sid string) error {
	if err := inspectDirectoryNoReparseChain(dataDir); err != nil {
		return ErrACLDrift
	}
	for _, path := range []string{filepath.Join(dataDir, "chuzi.db"), filepath.Join(dataDir, "backups")} {
		attrs, err := fileAttributes(path)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if err != nil || attrs&fileAttributeReparsePoint != 0 {
			return ErrACLDrift
		}
		if err := denyPathAccess(path, sid, attrs&fileAttributeDirectory != 0); err != nil {
			return err
		}
	}
	return nil
}

func denyPathAccess(path, sid string, inherit bool) error {
	security, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	if sddlHasFullDeny(security.String(), sid) {
		return nil
	}
	value, err := windows.StringToSid(sid)
	if err != nil {
		return ErrACLDrift
	}
	inheritance := uint32(0)
	if inherit {
		inheritance = subContainersAndObjectsInherit
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: windows.ACCESS_MASK(0x001f01ff), AccessMode: windows.ACCESS_MODE(denyAccess), Inheritance: inheritance, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_FORM(trusteeIsSid), TrusteeType: windows.TRUSTEE_TYPE(trusteeIsUser), TrusteeValue: windows.TrusteeValueFromSID(value)}}
	updated, err := windows.BuildSecurityDescriptor(nil, nil, []windows.EXPLICIT_ACCESS{entry}, nil, security)
	if err != nil {
		return ErrACLDrift
	}
	dacl, _, err := updated.DACL()
	if err != nil || dacl == nil {
		return ErrACLDrift
	}
	if err := windows.SetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation, nil, nil, dacl, nil); err != nil {
		return ErrACLDrift
	}
	return nil
}

func inspectSlotDirectories(paths Paths, sid string) error {
	serviceSID, err := currentProcessSID()
	if err != nil {
		return ErrACLDrift
	}
	for _, path := range []string{paths.Root, paths.Generation, paths.Work, paths.Temp, paths.Logs} {
		if err := inspectDirectoryNoReparseChain(path); err != nil {
			return err
		}
	}
	for path, scope := range map[string]string{paths.Root: "root", paths.Generation: "generation", paths.Work: "private", paths.Temp: "private", paths.Logs: "private"} {
		if err := verifyDirectoryACL(path, sid, serviceSID, scope); err != nil {
			return err
		}
	}
	if err := verifyControlPlaneFiles(filepath.Dir(filepath.Dir(paths.Root)), sid); err != nil {
		return err
	}
	return nil
}

func verifyControlPlaneFiles(dataDir, sid string) error {
	if err := inspectDirectoryNoReparseChain(dataDir); err != nil {
		return ErrACLDrift
	}
	for _, path := range []string{filepath.Join(dataDir, "chuzi.db"), filepath.Join(dataDir, "backups")} {
		attrs, err := fileAttributes(path)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if err != nil || attrs&fileAttributeReparsePoint != 0 {
			return ErrACLDrift
		}
		security, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
		if err != nil || !sddlHasFullDeny(security.String(), sid) {
			return ErrACLDrift
		}
	}
	return nil
}

func revokeControlPlaneFiles(dataDir, sid string) error {
	if err := inspectDirectoryNoReparseChain(dataDir); err != nil {
		return ErrACLDrift
	}
	for _, path := range []string{filepath.Join(dataDir, "chuzi.db"), filepath.Join(dataDir, "backups")} {
		attrs, err := fileAttributes(path)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if err != nil || attrs&fileAttributeReparsePoint != 0 {
			return ErrACLDrift
		}
		if err := revokePathAccess(path, sid, attrs&fileAttributeDirectory != 0); err != nil {
			return err
		}
	}
	return nil
}

func revokePathAccess(path, sid string, inherit bool) error {
	security, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	value, err := windows.StringToSid(sid)
	if err != nil {
		return ErrACLDrift
	}
	inheritance := uint32(0)
	if inherit {
		inheritance = subContainersAndObjectsInherit
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: windows.ACCESS_MASK(0x001f01ff), AccessMode: windows.ACCESS_MODE(revokeAccess), Inheritance: inheritance, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_FORM(trusteeIsSid), TrusteeType: windows.TRUSTEE_TYPE(trusteeIsUser), TrusteeValue: windows.TrusteeValueFromSID(value)}}
	updated, err := windows.BuildSecurityDescriptor(nil, nil, []windows.EXPLICIT_ACCESS{entry}, nil, security)
	if err != nil {
		return ErrACLDrift
	}
	dacl, _, err := updated.DACL()
	if err != nil || dacl == nil {
		return ErrACLDrift
	}
	if err := windows.SetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation, nil, nil, dacl, nil); err != nil {
		return ErrACLDrift
	}
	check, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil || strings.Contains(check.String(), ";;;"+sid+")") {
		return ErrACLDrift
	}
	return nil
}

func sddlHasFullDeny(sddl, sid string) bool {
	for start := strings.IndexByte(sddl, '('); start >= 0; {
		end := strings.IndexByte(sddl[start+1:], ')')
		if end < 0 {
			return false
		}
		end += start + 1
		fields := strings.Split(sddl[start+1:end], ";")
		if len(fields) == 6 && fields[0] == "D" && strings.EqualFold(fields[5], sid) {
			if mask, ok := runtimeACLMask(fields[2]); ok && mask&0x001f01ff == 0x001f01ff {
				return true
			}
		}
		next := end + 1
		if next >= len(sddl) {
			break
		}
		remaining := sddl[next:]
		index := strings.IndexByte(remaining, '(')
		if index < 0 {
			break
		}
		start = next + index
	}
	return false
}

func setDirectoryACL(path, sid, serviceSID, scope string) error {
	mask, inheritance := fileModifyMask, "OICI"
	if scope == "root" {
		mask, inheritance = fileTraverseMask, ""
	}
	if scope == "generation" {
		mask = fileReadExecuteMask
	}
	userACE := "(A;" + inheritance + ";0x" + fmt.Sprintf("%08x", mask) + ";;;" + sid + ")"
	serviceACE := ""
	if serviceSID != "S-1-5-18" {
		serviceACE = "(A;OICI;FA;;;" + serviceSID + ")"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)" + serviceACE + userACE)
	if err != nil {
		return ErrACLDrift
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return ErrACLDrift
	}
	if err := windows.SetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation, nil, nil, dacl, nil); err != nil {
		return ErrACLDrift
	}
	return nil
}

func verifyDirectoryACL(path, sid, serviceSID, scope string) error {
	sd, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	expected := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if serviceSID != "S-1-5-18" {
		expected += "(A;OICI;FA;;;" + serviceSID + ")"
	}
	if scope == "root" {
		expected += "(A;;0x00120020;;;" + sid + ")"
	} else if scope == "generation" {
		expected += "(A;OICI;0x001200a9;;;" + sid + ")"
	} else {
		expected += "(A;OICI;0x001301bf;;;" + sid + ")"
	}
	if !strings.Contains(sd.String(), expected) {
		return ErrACLDrift
	}
	return nil
}

func ensureNoReparseDirectory(path string) error {
	if err := ensureDirectoryChain(path); err != nil {
		return err
	}
	return inspectNoReparse(path)
}

func ensureDirectoryChain(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return ErrInvalidOptions
	}
	rest := strings.TrimLeft(strings.TrimPrefix(clean, volume), "\\/")
	current := volume + string(filepath.Separator)
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		current = filepath.Join(current, component)
		attrs, err := fileAttributes(current)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			wide, convErr := windows.UTF16PtrFromString(current)
			if convErr != nil {
				return ErrInvalidOptions
			}
			if createErr := windows.CreateDirectory(wide, nil); createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
				return ErrACLDrift
			}
			attrs, err = fileAttributes(current)
		}
		if err != nil || attrs&fileAttributeDirectory == 0 || attrs&fileAttributeReparsePoint != 0 {
			return ErrACLDrift
		}
	}
	return nil
}

// inspectNoReparseChain validates every existing component of a managed path.
// Checking only the final component is insufficient on Windows because a
// parent junction can redirect an otherwise ordinary-looking child path.
func inspectNoReparseChain(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return ErrACLDrift
	}
	rest := strings.TrimLeft(strings.TrimPrefix(clean, volume), `\/`)
	current := volume + string(filepath.Separator)
	if err := inspectNoReparse(current); err != nil {
		return err
	}
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		current = filepath.Join(current, component)
		if err := inspectNoReparse(current); err != nil {
			return err
		}
		attrs, err := fileAttributes(current)
		if err != nil {
			return ErrACLDrift
		}
		if attrs&fileAttributeDirectory == 0 && current != clean {
			return ErrACLDrift
		}
	}
	return nil
}

func inspectDirectoryNoReparseChain(path string) error {
	if err := inspectNoReparseChain(path); err != nil {
		return err
	}
	attrs, err := fileAttributes(filepath.Clean(path))
	if err != nil || attrs&fileAttributeDirectory == 0 {
		return ErrACLDrift
	}
	return nil
}

func inspectNoReparse(path string) error {
	attrs, err := fileAttributes(path)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return fs.ErrNotExist
		}
		return ErrACLDrift
	}
	if attrs&fileAttributeReparsePoint != 0 {
		return ErrACLDrift
	}
	return nil
}

func fileAttributes(path string) (uint32, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(wide)
}

func writeOwnership(path string, record ownershipRecord) error {
	if _, err := fileAttributes(path); err == nil {
		return ErrOwnership
	} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return ErrOwnership
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ErrOwnership
	}
	return writeServiceFile(path, data)
}

func writeServiceFile(path string, data []byte) error {
	if err := ensureDirectoryChain(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return ErrOwnership
	}
	return nil
}

func readOwnership(path string) (ownershipRecord, error) {
	var record ownershipRecord
	if err := inspectNoReparseChain(path); err != nil {
		return record, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if len(data) > 4096 {
		return record, ErrOwnership
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || record.Version != 1 || record.SlotID == "" || record.Ordinal < 1 || record.SID == "" || record.Generation == 0 {
		return ownershipRecord{}, ErrOwnership
	}
	return record, nil
}

func readProfileGrant(path string) (profileGrantRecord, error) {
	var record profileGrantRecord
	if err := inspectNoReparseChain(path); err != nil {
		return record, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if len(data) > 4096 {
		return record, ErrACLDrift
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || record.Version != 1 || record.Profile == "" {
		return profileGrantRecord{}, ErrACLDrift
	}
	return record, nil
}

func getUserInfo(username string) (userSnapshot, error) {
	name, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return userSnapshot{}, ErrOwnership
	}
	var buffer *byte
	if status := callNetUserGetInfo(name, &buffer); status != 0 {
		return userSnapshot{}, ErrOwnership
	}
	defer windows.NetApiBufferFree(buffer)
	info := (*userInfo1)(unsafe.Pointer(buffer))
	comment := ""
	if info.Comment != nil {
		comment = windows.UTF16PtrToString(info.Comment)
	}
	return userSnapshot{comment: comment, priv: info.Priv, flags: info.Flags}, nil
}

func callNetUserAdd(info *userInfo1) uint32 {
	proc := windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserAdd")
	var parameterError uint32
	status, _, _ := proc.Call(0, 1, uintptr(unsafe.Pointer(info)), uintptr(unsafe.Pointer(&parameterError)))
	return uint32(status)
}
func callNetUserGetInfo(name *uint16, buffer **byte) uint32 {
	proc := windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserGetInfo")
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(name)), 1, uintptr(unsafe.Pointer(buffer)))
	return uint32(status)
}
func callNetUserDelete(name *uint16) uint32 {
	proc := windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserDel")
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(name)))
	return uint32(status)
}

func deleteManagedUser(username, slotID string, ordinal int, expectedSID string) error {
	info, err := getUserInfo(username)
	if err != nil {
		return ErrCleanupUserInspect
	}
	if info.comment != fmt.Sprintf("CHUZI-MANAGED:%s:%d", slotID, ordinal) {
		return ErrCleanupUserMarker
	}
	if info.priv != userPrivUser {
		return ErrCleanupUserPrivilege
	}
	if info.flags&userFlagDisabled != 0 {
		return ErrCleanupUserDisabled
	}
	admin, adminErr := administratorsMembership(username)
	if adminErr != nil {
		return ErrCleanupUserInspect
	}
	if admin {
		return ErrCleanupUserAdmin
	}
	if expectedSID != "" {
		actualSID, sidErr := lookupSID(username)
		if errors.Is(sidErr, windows.ERROR_NONE_MAPPED) {
			return nil
		}
		if sidErr != nil {
			return ErrCleanupUserInspect
		}
		if actualSID != expectedSID {
			return ErrCleanupUserSID
		}
	}
	name, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return ErrCleanupUserInspect
	}
	status := callNetUserDelete(name)
	if status == netErrorUserNotFound {
		return nil
	}
	if status != 0 {
		return ErrCleanupUserDelete
	}
	return nil
}

// deleteManagedUserWithRetry handles the short interval in which Windows
// releases a just-logged-off user's profile and token handles. Ownership is
// checked on every attempt; only the deletion syscall is retried.
func deleteManagedUserWithRetry(ctx context.Context, username, slotID string, ordinal int, expectedSID string) error {
	return retryCleanup(ctx, func() error {
		return deleteManagedUser(username, slotID, ordinal, expectedSID)
	})
}

func deleteManagedProfileWithRetry(ctx context.Context, username, sid string) error {
	return retryCleanup(ctx, func() error {
		return deleteManagedProfile(username, sid)
	})
}

func retryCleanup(ctx context.Context, action func() error) error {
	if ctx == nil || action == nil {
		return ErrCleanup
	}
	const attempts = 6
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return ErrCleanup
		}
		if err := action(); err == nil {
			return nil
		} else if errors.Is(err, ErrOwnership) || errors.Is(err, ErrACLDrift) || errors.Is(err, ErrCleanupUserOwnership) || errors.Is(err, ErrCleanupUserMarker) || errors.Is(err, ErrCleanupUserPrivilege) || errors.Is(err, ErrCleanupUserDisabled) || errors.Is(err, ErrCleanupUserAdmin) || errors.Is(err, ErrCleanupUserSID) {
			return err
		} else {
			lastErr = err
		}
		if attempt == attempts-1 {
			return lastErr
		}
		delay := time.Duration(100*(1<<attempt)) * time.Millisecond
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if lastErr != nil {
				return lastErr
			}
			return ErrCleanup
		case <-timer.C:
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return ErrCleanup
}

func randomPasswordUTF16(length int) ([]uint16, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%+-_"
	if length < 32 || length > 128 {
		return nil, ErrInvalidOptions
	}
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		clear(random)
		return nil, err
	}
	password := make([]uint16, length+1)
	for i, value := range random {
		password[i] = uint16(alphabet[int(value)%len(alphabet)])
	}
	clear(random)
	return password, nil
}

func lookupSID(account string) (string, error) {
	name, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return "", err
	}
	sidSize, domainSize := uint32(0), uint32(0)
	var use uint32
	err = windows.LookupAccountName(nil, name, nil, &sidSize, nil, &domainSize, &use)
	if err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return "", err
	}
	sidBuffer := make([]byte, sidSize)
	domainBuffer := make([]uint16, domainSize+1)
	sid := (*windows.SID)(unsafe.Pointer(&sidBuffer[0]))
	if err := windows.LookupAccountName(nil, name, sid, &sidSize, &domainBuffer[0], &domainSize, &use); err != nil {
		return "", err
	}
	return sid.String(), nil
}

// lookupSIDEventually covers the short interval after NetUserAdd during
// which the local account is visible to NetAPI but not yet resolvable through
// LookupAccountName. The bounded retry keeps the identity fence fail-closed.
func lookupSIDEventually(account string) (string, error) {
	const attempts = 6
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		sid, err := lookupSID(account)
		if err == nil {
			return sid, nil
		}
		lastErr = err
		if attempt == attempts-1 {
			break
		}
		timer := time.NewTimer(time.Duration(100*(1<<attempt)) * time.Millisecond)
		<-timer.C
	}
	return "", lastErr
}

func currentProcessSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user.User.Sid == nil {
		return "", ErrSessionIdentity
	}
	return user.User.Sid.String(), nil
}

func groupNameForSID(value string) (string, error) {
	sid, err := windows.StringToSid(value)
	if err != nil {
		return "", err
	}
	nameSize, domainSize := uint32(0), uint32(0)
	var use uint32
	err = windows.LookupAccountSid(nil, sid, nil, &nameSize, nil, &domainSize, &use)
	if err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return "", err
	}
	name := make([]uint16, nameSize+1)
	domain := make([]uint16, domainSize+1)
	if err := windows.LookupAccountSid(nil, sid, &name[0], &nameSize, &domain[0], &domainSize, &use); err != nil {
		return "", err
	}
	return windows.UTF16ToString(name[:nameSize]), nil
}

func localGroupNames(username string) ([]string, error) {
	name, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return nil, err
	}
	proc := windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserGetLocalGroups")
	var buffer *byte
	var read, total uint32
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(name)), 0, localGroupIncludeIndirect, uintptr(unsafe.Pointer(&buffer)), ^uintptr(0), uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)))
	if status != 0 {
		return nil, ErrOwnership
	}
	defer windows.NetApiBufferFree(buffer)
	entries := unsafe.Slice((*localGroupUsers0)(unsafe.Pointer(buffer)), read)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name != nil {
			names = append(names, windows.UTF16PtrToString(entry.Name))
		}
	}
	return names, nil
}

func hasAdministratorsMembership(username string) bool {
	member, err := administratorsMembership(username)
	return err != nil || member
}

func administratorsMembership(username string) (bool, error) {
	const administratorsSID = "S-1-5-32-544"
	groups, err := localGroupNames(username)
	if err != nil {
		return false, err
	}
	for _, group := range groups {
		// Compare the well-known SID instead of the localized group name.
		// LookupAccountSid can fail for a localized/buffered name even when
		// local-group enumeration itself is valid.
		sid, err := lookupSID(group)
		if err != nil {
			return false, err
		}
		if sid == administratorsSID {
			return true, nil
		}
	}
	return false, nil
}

func ensureRemoteDesktopMembership(username string) error {
	return changeRemoteDesktopMembership(username, true)
}
func verifyRemoteDesktopMembership(username string) error {
	groups, err := localGroupNames(username)
	if err != nil {
		return ErrSessionGroupVerify
	}
	userSID, err := lookupSID(username)
	if err != nil {
		return ErrSessionUserLookup
	}
	memberOfRDP := false
	principals := make([]string, 0, len(groups))
	for _, name := range groups {
		groupSID, sidErr := lookupSID(name)
		if sidErr != nil {
			return ErrSessionGroupVerify
		}
		if groupSID == "S-1-5-32-555" {
			memberOfRDP = true
		}
		principals = append(principals, groupSID)
	}
	if !memberOfRDP {
		return ErrSessionGroupVerify
	}
	if err := verifyRemoteInteractiveRight(userSID, "S-1-5-32-555", principals...); err != nil {
		return ErrSessionPolicy
	}
	return nil
}

func hasGroup(groups []string, expected string) bool {
	for _, value := range groups {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

type lsaUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type lsaObjectAttributes struct {
	Length                   uint32
	RootDirectory            uintptr
	ObjectName               uintptr
	Attributes               uint32
	SecurityDescriptor       uintptr
	SecurityQualityOfService uintptr
}

type lsaEnumerationInformation struct {
	SID *windows.SID
}

func verifyRemoteInteractiveRight(userSID, groupSID string, additionalGroups ...string) error {
	allowed, err := lsaRightSIDs("SeRemoteInteractiveLogonRight")
	if err != nil {
		return ErrSessionPolicy
	}
	principals := append([]string{userSID, groupSID}, additionalGroups...)
	allow := false
	for _, principal := range principals {
		if allowed[principal] {
			allow = true
			break
		}
	}
	if !allow {
		return ErrSessionPolicy
	}
	denied, err := lsaRightSIDs("SeDenyRemoteInteractiveLogonRight")
	if err != nil {
		return ErrSessionPolicy
	}
	// An explicit deny for the managed user, any local group, or Everyone
	// must never be masked by membership in Remote Desktop Users.
	for _, principal := range principals {
		if denied[principal] {
			return ErrSessionPolicy
		}
	}
	for _, everyone := range []string{"S-1-1-0", "S-1-5-11"} {
		if denied[everyone] {
			return ErrSessionPolicy
		}
	}
	return nil
}

const (
	statusObjectNameNotFound = 0xC0000034
	statusNoMoreEntries      = 0x8000001A
)

func lsaRightSIDs(name string) (map[string]bool, error) {
	rightText, err := windows.UTF16FromString(name)
	if err != nil || len(rightText) < 2 {
		return nil, ErrSessionIdentity
	}
	right := lsaUnicodeString{Length: uint16((len(rightText) - 1) * 2), MaximumLength: uint16(len(rightText) * 2), Buffer: &rightText[0]}
	attrs := lsaObjectAttributes{Length: uint32(unsafe.Sizeof(lsaObjectAttributes{}))}
	open := windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaOpenPolicy")
	enumerate := windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaEnumerateAccountsWithUserRight")
	free := windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaFreeMemory")
	close := windows.NewLazySystemDLL("advapi32.dll").NewProc("LsaClose")
	var policy uintptr
	status, _, _ := open.Call(0, uintptr(unsafe.Pointer(&attrs)), 0x00000800, uintptr(unsafe.Pointer(&policy)))
	if status != 0 || policy == 0 {
		return nil, ErrSessionIdentity
	}
	defer close.Call(policy)
	var entries *lsaEnumerationInformation
	var count uint32
	status, _, _ = enumerate.Call(policy, uintptr(unsafe.Pointer(&right)), uintptr(unsafe.Pointer(&entries)), uintptr(unsafe.Pointer(&count)))
	if status == statusObjectNameNotFound || status == statusNoMoreEntries {
		return map[string]bool{}, nil
	}
	if status != 0 {
		return nil, ErrSessionIdentity
	}
	if entries == nil || count == 0 {
		return map[string]bool{}, nil
	}
	defer free.Call(uintptr(unsafe.Pointer(entries)))
	result := make(map[string]bool, count)
	for _, entry := range unsafe.Slice(entries, count) {
		if entry.SID == nil {
			continue
		}
		result[entry.SID.String()] = true
	}
	return result, nil
}

func changeRemoteDesktopMembership(username string, add bool) error {
	if err := verifyRemoteDesktopMembership(username); err == nil && add {
		return nil
	}
	group, err := groupNameForSID("S-1-5-32-555")
	if err != nil {
		return ErrSessionGroupLookup
	}
	groupPtr, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return ErrSessionIdentity
	}
	sidText, err := lookupSID(username)
	if err != nil {
		return ErrSessionUserLookup
	}
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return ErrSessionIdentity
	}
	member := localGroupMember0{SID: sid}
	procName := "NetLocalGroupAddMembers"
	if !add {
		procName = "NetLocalGroupDelMembers"
	}
	proc := windows.NewLazySystemDLL("netapi32.dll").NewProc(procName)
	status, _, _ := proc.Call(0, uintptr(unsafe.Pointer(groupPtr)), 0, uintptr(unsafe.Pointer(&member)), 1)
	if add && uint32(status) == netErrorMemberInAlias {
		return nil
	}
	if status != 0 {
		return ErrSessionGroupAdd
	}
	return nil
}

func changeProfileACL(path, sidText string, grant bool) error {
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		return ErrACLDrift
	}
	old, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	accessMode := grantAccess
	if !grant {
		accessMode = revokeAccess
	}
	entry := windows.EXPLICIT_ACCESS{AccessPermissions: windows.ACCESS_MASK(fileModifyMask), AccessMode: windows.ACCESS_MODE(accessMode), Inheritance: subContainersAndObjectsInherit, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_FORM(trusteeIsSid), TrusteeType: windows.TRUSTEE_TYPE(trusteeIsUser), TrusteeValue: windows.TrusteeValueFromSID(sid)}}
	updated, err := windows.BuildSecurityDescriptor(nil, nil, []windows.EXPLICIT_ACCESS{entry}, nil, old)
	if err != nil {
		return ErrACLDrift
	}
	dacl, _, err := updated.DACL()
	if err != nil || dacl == nil {
		return ErrACLDrift
	}
	if err := windows.SetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation, nil, nil, dacl, nil); err != nil {
		return ErrACLDrift
	}
	return nil
}

func verifyProfileACL(path, sidText string) error {
	sd, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	text := sd.String()
	if !strings.Contains(text, "0x001301bf;;;"+sidText+")") || strings.Contains(text, "(D;") && strings.Contains(text, ";;;"+sidText+")") {
		return ErrACLDrift
	}
	return nil
}

func verifyProfileACLAbsent(path, sidText string) error {
	sd, err := windows.GetNamedSecurityInfo(path, seFileObject, seDaclSecurityInformation)
	if err != nil {
		return ErrACLDrift
	}
	if strings.Contains(sd.String(), ";;;"+sidText+")") {
		return ErrACLDrift
	}
	return nil
}

func logoffManagedSessions(ctx context.Context, sid string) error {
	for {
		if ctx.Err() != nil {
			return ErrCleanup
		}
		var raw *windows.WTS_SESSION_INFO
		var count uint32
		if err := windows.WTSEnumerateSessions(0, 0, 1, &raw, &count); err != nil {
			return ErrCleanup
		}
		entries := unsafe.Slice(raw, count)
		matched := make([]uint32, 0, 2)
		for _, entry := range entries {
			if entry.SessionID == 0 {
				continue
			}
			token, err := querySessionToken(entry.SessionID)
			if err != nil {
				continue
			}
			user, userErr := token.GetTokenUser()
			_ = token.Close()
			if userErr == nil && user.User.Sid.String() == sid {
				matched = append(matched, entry.SessionID)
			}
		}
		windows.WTSFreeMemory(uintptr(unsafe.Pointer(raw)))
		if len(matched) == 0 {
			return nil
		}
		for _, id := range matched {
			if err := wtsLogoffSession(id); err != nil {
				return ErrCleanup
			}
		}
		select {
		case <-ctx.Done():
			return ErrCleanup
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func querySessionToken(sessionID uint32) (windows.Token, error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		return 0, err
	}
	return token, nil
}
func wtsLogoffSession(sessionID uint32) error {
	proc := windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSLogoffSession")
	r1, _, callErr := proc.Call(0, uintptr(sessionID), 0)
	if r1 == 0 {
		return callErr
	}
	return nil
}
func deleteManagedProfile(username, sid string) error {
	if sid == "" {
		return ErrCleanup
	}
	if _, err := windows.StringToSid(sid); err != nil {
		return ErrOwnership
	}
	drive := os.Getenv("SystemDrive")
	if drive == "" || filepath.VolumeName(drive) == "" || strings.ContainsAny(username, "\\/:*?\"<>|") {
		return ErrCleanup
	}
	profile := filepath.Join(drive+string(filepath.Separator), "Users", username)
	if err := inspectDirectoryNoReparseChain(profile); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return ErrCleanup
	}
	sidWide, err := windows.UTF16PtrFromString(sid)
	if err != nil {
		return ErrCleanup
	}
	profileWide, err := windows.UTF16PtrFromString(profile)
	if err != nil {
		return ErrCleanup
	}
	proc := windows.NewLazySystemDLL("userenv.dll").NewProc("DeleteProfileW")
	// DeleteProfileW takes the SID first and the optional profile path second.
	// Passing the path as lpSidString leaves the profile loaded and makes the
	// subsequent NetUserDel fail with a transient profile-in-use error.
	r1, _, callErr := proc.Call(uintptr(unsafe.Pointer(sidWide)), uintptr(unsafe.Pointer(profileWide)), 0)
	if r1 == 0 && !errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) {
		return ErrCleanup
	}
	return nil
}

func removeOwnedTree(root string) error {
	if err := inspectDirectoryNoReparseChain(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return ErrCleanup
	}
	if err := validateOwnedTreeShape(root); err != nil {
		return ErrCleanup
	}
	if err := removeOwnedPath(root); err != nil {
		return ErrCleanup
	}
	return nil
}

// validateOwnedTreeShape is a deletion guard. Slot roots may contain only
// service-owned metadata and generation directories with the fixed work/tmp/logs
// layout. Unknown root entries are never recursively deleted.
func validateOwnedTreeShape(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		if err := inspectNoReparseChain(path); err != nil {
			return err
		}
		if managedGenerationPattern.MatchString(name) {
			if !entry.IsDir() || !validateGenerationTree(path) {
				return ErrCleanup
			}
			continue
		}
		switch name {
		case "ownership.json", "profile-access.json", "account.dpapi":
			if entry.IsDir() {
				return ErrCleanup
			}
		default:
			return ErrCleanup
		}
	}
	return nil
}

func validateGenerationTree(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	seen := make(map[string]bool, 3)
	for _, entry := range entries {
		if entry.Name() != "work" && entry.Name() != "tmp" && entry.Name() != "logs" {
			return false
		}
		if !entry.IsDir() || seen[entry.Name()] {
			return false
		}
		if err := inspectNoReparseChain(filepath.Join(path, entry.Name())); err != nil {
			return false
		}
		seen[entry.Name()] = true
	}
	return len(seen) == 3
}

// removeOwnedPath walks and removes each entry after checking it immediately
// before use. It avoids RemoveAll's recursive follow behavior if an attacker
// replaces a managed entry with a junction or reparse point between the
// initial validation and deletion.
func removeOwnedPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	attrs, err := fileAttributes(path)
	if err != nil || attrs&fileAttributeReparsePoint != 0 || info.Mode()&os.ModeSymlink != 0 {
		return ErrCleanup
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeOwnedPath(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(path)
}
