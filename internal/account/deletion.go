package account

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type DeletionStage string

const (
	DeletionRequested           DeletionStage = "REQUESTED"
	DeletionStoppingSessions    DeletionStage = "STOPPING_SESSIONS"
	DeletionRevokingCredentials DeletionStage = "REVOKING_CREDENTIALS"
	DeletionCleaningProfile     DeletionStage = "CLEANING_PROFILE"
	DeletionRetryWait           DeletionStage = "RETRY_WAIT"
	DeletionBlocked             DeletionStage = "BLOCKED"
	DeletionTombstoned          DeletionStage = "TOMBSTONED"
)

type ProfilePolicy string

const (
	ProfilePurge  ProfilePolicy = "purge"
	ProfileRetain ProfilePolicy = "retain"
)

var (
	ErrInvalidDeletion           = errors.New("account: invalid deletion")
	ErrDeletionEventConflict     = errors.New("account: deletion event conflict")
	ErrDeletionStaleEvent        = errors.New("account: stale deletion event")
	ErrDeletionInvalidTransition = errors.New("account: invalid deletion transition")
	ErrDeletionTerminal          = errors.New("account: deletion is terminal")
)

type DeletionSnapshot struct {
	DeletionID           string                   `json:"deletion_id"`
	AccountID            string                   `json:"account_id"`
	AccountLabel         string                   `json:"account_label"`
	Stage                DeletionStage            `json:"stage"`
	LastBusinessStatus   Status                   `json:"last_business_status"`
	Actor                string                   `json:"actor"`
	ReasonClass          string                   `json:"reason_class"`
	ProfilePolicy        ProfilePolicy            `json:"profile_policy"`
	Revision             uint64                   `json:"revision"`
	RequestedAt          time.Time                `json:"requested_at"`
	UpdatedAt            time.Time                `json:"updated_at"`
	CompletedAt          time.Time                `json:"completed_at,omitempty"`
	Attempt              int                      `json:"attempt"`
	NextAttemptAt        time.Time                `json:"next_attempt_at"`
	LastFailureClass     FailureClass             `json:"last_failure_class,omitempty"`
	SessionsStopped      bool                     `json:"sessions_stopped"`
	CredentialsRevoked   bool                     `json:"credentials_revoked"`
	ProfileActionApplied bool                     `json:"profile_action_applied"`
	AppliedEvents        map[string]DeletionAudit `json:"applied_events"`
}

type DeletionAudit struct {
	Event    DeletionEvent `json:"event"`
	Revision uint64        `json:"revision"`
}

type DeletionEvent struct {
	EventID              string        `json:"event_id"`
	DeletionID           string        `json:"deletion_id"`
	AccountID            string        `json:"account_id"`
	Actor                string        `json:"actor"`
	ReasonClass          string        `json:"reason_class"`
	ExpectedRevision     uint64        `json:"expected_revision"`
	From                 DeletionStage `json:"from"`
	To                   DeletionStage `json:"to"`
	OccurredAt           time.Time     `json:"occurred_at"`
	Attempt              int           `json:"attempt"`
	NextAttemptAt        time.Time     `json:"next_attempt_at"`
	LastFailureClass     FailureClass  `json:"last_failure_class,omitempty"`
	SessionsStopped      bool          `json:"sessions_stopped"`
	CredentialsRevoked   bool          `json:"credentials_revoked"`
	ProfileActionApplied bool          `json:"profile_action_applied"`
}

func NewDeletion(deletionID, accountID, accountLabel, actor, reasonClass string, policy ProfilePolicy, lastStatus Status, at time.Time) (DeletionSnapshot, error) {
	state := DeletionSnapshot{DeletionID: deletionID, AccountID: accountID, AccountLabel: accountLabel, Stage: DeletionRequested, LastBusinessStatus: lastStatus, Actor: actor, ReasonClass: reasonClass, ProfilePolicy: policy, RequestedAt: at, UpdatedAt: at, AppliedEvents: make(map[string]DeletionAudit)}
	if err := state.Validate(); err != nil {
		return DeletionSnapshot{}, err
	}
	return state, nil
}

func (s DeletionSnapshot) Validate() error {
	if !validDeletionStage(s.Stage) || !nonEmptyDeletion(s.DeletionID) || !nonEmptyDeletion(s.AccountID) || !nonEmptyDeletion(s.AccountLabel) || !nonEmptyDeletion(s.Actor) || !nonEmptyDeletion(s.ReasonClass) || !s.LastBusinessStatus.Valid() || (s.ProfilePolicy != ProfilePurge && s.ProfilePolicy != ProfileRetain) || s.RequestedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.RequestedAt) || s.Attempt < 0 || !validFailureClassDeletion(s.LastFailureClass) {
		return ErrInvalidDeletion
	}
	if s.Stage == DeletionTombstoned && s.CompletedAt.IsZero() {
		return fmt.Errorf("%w: tombstone requires completion time", ErrInvalidDeletion)
	}
	if s.Stage == DeletionTombstoned && !s.CompletedAt.Equal(s.UpdatedAt) {
		return fmt.Errorf("%w: tombstone completion must match update time", ErrInvalidDeletion)
	}
	if s.Stage != DeletionTombstoned && !s.CompletedAt.IsZero() {
		return fmt.Errorf("%w: incomplete deletion has completion time", ErrInvalidDeletion)
	}
	if !s.NextAttemptAt.IsZero() && s.NextAttemptAt.Before(s.UpdatedAt) {
		return fmt.Errorf("%w: next attempt precedes update", ErrInvalidDeletion)
	}
	if s.AppliedEvents == nil {
		return fmt.Errorf("%w: missing event index", ErrInvalidDeletion)
	}
	if s.CredentialsRevoked && !s.SessionsStopped {
		return fmt.Errorf("%w: credentials checkpoint requires stopped sessions", ErrInvalidDeletion)
	}
	if s.ProfileActionApplied && !s.CredentialsRevoked {
		return fmt.Errorf("%w: profile checkpoint requires revoked credentials", ErrInvalidDeletion)
	}
	if s.Stage == DeletionTombstoned && (!s.SessionsStopped || !s.CredentialsRevoked || !s.ProfileActionApplied) {
		return fmt.Errorf("%w: tombstone requires all checkpoints", ErrInvalidDeletion)
	}
	for id, audit := range s.AppliedEvents {
		if id == "" || audit.Revision == 0 || audit.Revision > s.Revision || audit.Event.EventID != id || audit.Event.DeletionID != s.DeletionID || audit.Event.AccountID != s.AccountID {
			return fmt.Errorf("%w: invalid event index", ErrInvalidDeletion)
		}
		if err := audit.Event.Validate(); err != nil {
			return err
		}
	}
	if uint64(len(s.AppliedEvents)) != s.Revision {
		return fmt.Errorf("%w: revision %d does not match event count %d", ErrInvalidDeletion, s.Revision, len(s.AppliedEvents))
	}
	return nil
}

func (e DeletionEvent) Validate() error {
	if !nonEmptyDeletion(e.EventID) || !nonEmptyDeletion(e.DeletionID) || !nonEmptyDeletion(e.AccountID) || !nonEmptyDeletion(e.Actor) || !nonEmptyDeletion(e.ReasonClass) || !validDeletionStage(e.From) || !validDeletionStage(e.To) || e.OccurredAt.IsZero() || !validFailureClassDeletion(e.LastFailureClass) || e.Attempt < 0 {
		return ErrInvalidDeletion
	}
	if !e.NextAttemptAt.IsZero() && e.NextAttemptAt.Before(e.OccurredAt) {
		return fmt.Errorf("%w: next attempt precedes event", ErrInvalidDeletion)
	}
	return nil
}

func (e DeletionEvent) equal(other DeletionEvent) bool {
	return e.EventID == other.EventID && e.DeletionID == other.DeletionID && e.AccountID == other.AccountID && e.Actor == other.Actor && e.ReasonClass == other.ReasonClass && e.ExpectedRevision == other.ExpectedRevision && e.From == other.From && e.To == other.To && e.OccurredAt.Equal(other.OccurredAt) && e.Attempt == other.Attempt && e.NextAttemptAt.Equal(other.NextAttemptAt) && e.LastFailureClass == other.LastFailureClass && e.SessionsStopped == other.SessionsStopped && e.CredentialsRevoked == other.CredentialsRevoked && e.ProfileActionApplied == other.ProfileActionApplied
}

func CanDeleteTransition(from, to DeletionStage) bool {
	if !validDeletionStage(from) || !validDeletionStage(to) {
		return false
	}
	switch from {
	case DeletionRequested:
		return to == DeletionStoppingSessions || to == DeletionBlocked
	case DeletionStoppingSessions:
		return to == DeletionRevokingCredentials || to == DeletionRetryWait || to == DeletionBlocked
	case DeletionRevokingCredentials:
		return to == DeletionCleaningProfile || to == DeletionRetryWait || to == DeletionBlocked
	case DeletionCleaningProfile:
		return to == DeletionTombstoned || to == DeletionRetryWait || to == DeletionBlocked
	case DeletionRetryWait:
		return to == DeletionStoppingSessions || to == DeletionRevokingCredentials || to == DeletionCleaningProfile || to == DeletionBlocked
	case DeletionBlocked:
		return to == DeletionStoppingSessions || to == DeletionRevokingCredentials || to == DeletionCleaningProfile
	default:
		return false
	}
}

type DeletionTransitionResult struct {
	State      DeletionSnapshot
	Audit      DeletionAudit
	Idempotent bool
}

func ApplyDeletion(state DeletionSnapshot, event DeletionEvent) (DeletionTransitionResult, error) {
	if err := state.Validate(); err != nil {
		return DeletionTransitionResult{}, err
	}
	if err := event.Validate(); err != nil {
		return DeletionTransitionResult{}, err
	}
	if event.DeletionID != state.DeletionID || event.AccountID != state.AccountID {
		return DeletionTransitionResult{}, ErrInvalidDeletion
	}
	if previous, ok := state.AppliedEvents[event.EventID]; ok {
		if previous.Event.equal(event) {
			return DeletionTransitionResult{State: state.Clone(), Audit: previous, Idempotent: true}, nil
		}
		return DeletionTransitionResult{}, fmt.Errorf("%w: %s", ErrDeletionEventConflict, event.EventID)
	}
	if state.Stage == DeletionTombstoned {
		return DeletionTransitionResult{}, ErrDeletionTerminal
	}
	if event.ExpectedRevision != state.Revision || event.From != state.Stage {
		return DeletionTransitionResult{}, ErrDeletionStaleEvent
	}
	if !CanDeleteTransition(event.From, event.To) {
		return DeletionTransitionResult{}, ErrDeletionInvalidTransition
	}
	if event.OccurredAt.Before(state.UpdatedAt) {
		return DeletionTransitionResult{}, fmt.Errorf("%w: event time precedes state update", ErrDeletionStaleEvent)
	}
	if err := validateDeletionCheckpoints(state, event); err != nil {
		return DeletionTransitionResult{}, err
	}
	if state.Revision == ^uint64(0) {
		return DeletionTransitionResult{}, ErrRevisionOverflow
	}
	next := state.Clone()
	next.Stage = event.To
	next.Revision++
	next.UpdatedAt = event.OccurredAt
	next.Attempt = event.Attempt
	next.NextAttemptAt = event.NextAttemptAt
	next.LastFailureClass = event.LastFailureClass
	next.SessionsStopped = next.SessionsStopped || event.SessionsStopped
	next.CredentialsRevoked = next.CredentialsRevoked || event.CredentialsRevoked
	next.ProfileActionApplied = next.ProfileActionApplied || event.ProfileActionApplied
	if event.To == DeletionTombstoned {
		next.CompletedAt = event.OccurredAt
		next.NextAttemptAt = time.Time{}
		next.LastFailureClass = ""
	}
	audit := DeletionAudit{Event: event, Revision: next.Revision}
	next.AppliedEvents[event.EventID] = audit
	return DeletionTransitionResult{State: next, Audit: audit}, nil
}

func (s DeletionSnapshot) Clone() DeletionSnapshot {
	clone := s
	clone.AppliedEvents = make(map[string]DeletionAudit, len(s.AppliedEvents))
	for id, audit := range s.AppliedEvents {
		clone.AppliedEvents[id] = audit
	}
	return clone
}

func validateDeletionCheckpoints(state DeletionSnapshot, event DeletionEvent) error {
	sessionsStopped := state.SessionsStopped || event.SessionsStopped
	credentialsRevoked := state.CredentialsRevoked || event.CredentialsRevoked
	profileActionApplied := state.ProfileActionApplied || event.ProfileActionApplied
	if credentialsRevoked && !sessionsStopped {
		return fmt.Errorf("%w: credentials checkpoint requires stopped sessions", ErrDeletionInvalidTransition)
	}
	if profileActionApplied && !credentialsRevoked {
		return fmt.Errorf("%w: profile checkpoint requires revoked credentials", ErrDeletionInvalidTransition)
	}
	if event.To == DeletionCleaningProfile && !credentialsRevoked {
		return fmt.Errorf("%w: cleaning profile requires revoked credentials", ErrDeletionInvalidTransition)
	}
	if event.To == DeletionTombstoned && (!sessionsStopped || !credentialsRevoked || !profileActionApplied) {
		return fmt.Errorf("%w: tombstone requires all checkpoints", ErrDeletionInvalidTransition)
	}
	if (state.Stage == DeletionRetryWait || state.Stage == DeletionBlocked) && isDeletionCheckpointStage(event.To) {
		want := deletionCheckpointStage(state)
		if event.To != want {
			return fmt.Errorf("%w: retry must resume at %s", ErrDeletionInvalidTransition, want)
		}
	}
	return nil
}

func isDeletionCheckpointStage(stage DeletionStage) bool {
	switch stage {
	case DeletionStoppingSessions, DeletionRevokingCredentials, DeletionCleaningProfile:
		return true
	default:
		return false
	}
}

func deletionCheckpointStage(state DeletionSnapshot) DeletionStage {
	switch {
	case !state.SessionsStopped:
		return DeletionStoppingSessions
	case !state.CredentialsRevoked:
		return DeletionRevokingCredentials
	default:
		return DeletionCleaningProfile
	}
}

func validDeletionStage(stage DeletionStage) bool {
	switch stage {
	case DeletionRequested, DeletionStoppingSessions, DeletionRevokingCredentials, DeletionCleaningProfile, DeletionRetryWait, DeletionBlocked, DeletionTombstoned:
		return true
	default:
		return false
	}
}

func validFailureClassDeletion(class FailureClass) bool {
	if class == "" {
		return true
	}
	switch class {
	case TransientFailure, CredentialFailure, PermissionFailure, ConfigurationFailure, UnknownFailure:
		return true
	default:
		return false
	}
}

func nonEmptyDeletion(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 256 && !strings.ContainsAny(value, string([]byte{13, 10}))
}
