// Package store provides the single-node durable boundary for chuzi.
//
// It uses bbolt transactions for atomic account projection, audit, event
// idempotency, request projection, and lease updates. Business-state legality
// remains exclusively in internal/account.
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrInvalidAccount         = errors.New("store: invalid account")
	ErrAccountExists          = errors.New("store: account already exists")
	ErrAccountNotFound        = errors.New("store: account not found")
	ErrAccountBusy            = errors.New("store: account already has an active lifecycle")
	ErrInvalidRequest         = errors.New("store: invalid request")
	ErrRequestExists          = errors.New("store: request already exists")
	ErrRequestNotFound        = errors.New("store: request not found")
	ErrRequestConflict        = errors.New("store: request conflict")
	ErrIdempotencyConflict    = errors.New("store: idempotency key conflict")
	ErrRequestStateMismatch   = errors.New("store: request state mismatch")
	ErrAuditNotFound          = errors.New("store: audit record not found")
	ErrLeaseNotFound          = errors.New("store: lease not found")
	ErrBackupExists           = errors.New("store: backup already exists")
	ErrCorruptData            = errors.New("store: corrupt data")
	ErrQueueEmpty             = errors.New("store: queue is empty")
	ErrQueueCapacity          = errors.New("store: queue capacity is reached")
	ErrInvalidQueueOptions    = errors.New("store: invalid queue options")
	ErrRequestAttemptOverflow = errors.New("store: request attempt overflow")
)

// Request is the durable request projection. State mirrors the account state
// after an accepted event; a newly-created request starts at NO_REQUEST until
// its queue event is applied.
type Request struct {
	RequestID            string               `json:"request_id"`
	AccountID            string               `json:"account_id"`
	IdempotencyKey       string               `json:"idempotency_key"`
	NotificationRoomID   string               `json:"notification_room_id,omitempty"`
	State                account.Status       `json:"state"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
	Attempt              int                  `json:"attempt"`
	NotBefore            time.Time            `json:"not_before,omitempty"`
	Deadline             time.Time            `json:"deadline,omitempty"`
	LastFailure          account.FailureClass `json:"last_failure,omitempty"`
}

// NewRequest creates the initial request projection for an account.
func NewRequest(requestID, accountID, idempotencyKey string, createdAt time.Time) (Request, error) {
	request := Request{
		RequestID:      requestID,
		AccountID:      accountID,
		IdempotencyKey: idempotencyKey,
		State:          account.NoRequest,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
		NotBefore:      createdAt,
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Validate checks a request loaded from a caller or persistence.
func (r Request) Validate() error {
	if strings.TrimSpace(r.RequestID) == "" ||
		strings.TrimSpace(r.AccountID) == "" ||
		strings.TrimSpace(r.IdempotencyKey) == "" ||
		!r.State.Valid() || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return ErrInvalidRequest
	}
	if r.UpdatedAt.Before(r.CreatedAt) {
		return fmt.Errorf("%w: updated_at precedes created_at", ErrInvalidRequest)
	}
	if r.NotificationRoomID != "" && !safeNotificationRoom(r.NotificationRoomID) {
		return fmt.Errorf("%w: invalid notification room", ErrInvalidRequest)
	}
	if r.Attempt < 0 {
		return fmt.Errorf("%w: attempt must not be negative", ErrInvalidRequest)
	}
	if !r.NotBefore.IsZero() && r.NotBefore.Before(r.CreatedAt) {
		return fmt.Errorf("%w: not_before precedes created_at", ErrInvalidRequest)
	}
	if !r.Deadline.IsZero() && r.Deadline.Before(r.CreatedAt) {
		return fmt.Errorf("%w: deadline precedes created_at", ErrInvalidRequest)
	}
	if r.LastFailure != "" && !validFailureClass(r.LastFailure) {
		return fmt.Errorf("%w: invalid failure class", ErrInvalidRequest)
	}
	return nil
}

func validFailureClass(class account.FailureClass) bool {
	switch class {
	case account.TransientFailure, account.CredentialFailure,
		account.PermissionFailure, account.ConfigurationFailure,
		account.UnknownFailure:
		return true
	default:
		return false
	}
}

// QueueOptions controls one atomic queue claim. MaxGlobalConcurrency must be
// positive; an active lease counts as one running request.
type QueueOptions struct {
	MaxGlobalConcurrency int
}

// Claim is the durable result of moving one request from QUEUED to STARTING.
// The lease and transition are committed in the same bbolt transaction.
type Claim struct {
	Request    Request
	Lease      account.Lease
	Transition account.TransitionResult
}

// Store owns one configured single-node bbolt database.
type Store struct {
	db        *bbolt.DB
	cfg       config.Config
	closeOnce sync.Once
}

// Open creates the configured data directory, opens its derived database, and
// applies all repeatable schema migrations before returning.
func Open(cfg config.Config) (*Store, error) {
	normalized, err := config.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(normalized.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := bbolt.Open(normalized.DatabasePath(), 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := migrations.Apply(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	if err := rebuildQueueIndex(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("rebuild request queue index: %w", err)
	}
	return &Store{db: db, cfg: normalized}, nil
}

// Close releases the database file lock. It is safe to call more than once.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() { err = s.db.Close() })
	return err
}

// Config returns the normalized configuration used to open the store.
func (s *Store) Config() config.Config {
	if s == nil {
		return config.Config{}
	}
	return s.cfg
}

// SchemaVersion returns the durable schema version after migrations.
func (s *Store) SchemaVersion() (uint64, error) {
	if s == nil || s.db == nil {
		return 0, bbolt.ErrDatabaseNotOpen
	}
	return migrations.Version(s.db)
}

// CreateAccount adds a new account with a zero-revision NO_REQUEST snapshot.
func (s *Store) CreateAccount(accountID string) (account.Snapshot, error) {
	initial, err := account.NewSnapshot(accountID)
	if err != nil {
		return account.Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidAccount, err)
	}
	if err := s.update(func(tx *bbolt.Tx) error {
		accounts := tx.Bucket([]byte(migrations.AccountsBucket))
		if accounts.Get([]byte(accountID)) != nil {
			return ErrAccountExists
		}
		return putAccountProjection(accounts, initial)
	}); err != nil {
		return account.Snapshot{}, err
	}
	return initial, nil
}

// GetAccount reconstructs and validates an account from its projection and
// immutable audit history.
func (s *Store) GetAccount(accountID string) (account.Snapshot, error) {
	var result account.Snapshot
	err := s.view(func(tx *bbolt.Tx) error {
		var err error
		result, err = snapshotFromTx(tx, accountID)
		return err
	})
	return result, err
}

// ApplyEvent atomically applies one account event, writes its audit record and
// event index, updates the account projection, and updates the request
// projection. A duplicate event returns the original result without writing.
func (s *Store) ApplyEvent(event account.Event) (account.TransitionResult, error) {
	var result account.TransitionResult
	err := s.update(func(tx *bbolt.Tx) error {
		var err error
		result, err = applyEventTx(tx, event)
		return err
	})
	if err != nil {
		return account.TransitionResult{}, err
	}
	return result, nil
}

// applyEventTx applies one account event and all related projections inside an
// existing write transaction. Callers use this for compound queue operations.
func applyEventTx(tx *bbolt.Tx, event account.Event) (account.TransitionResult, error) {
	if raw := tx.Bucket([]byte(migrations.EventsBucket)).Get([]byte(event.EventID)); raw != nil {
		var previous account.AuditRecord
		if err := decode(raw, &previous); err != nil {
			return account.TransitionResult{}, err
		}
		if !eventsEqual(previous.Event, event) {
			return account.TransitionResult{}, fmt.Errorf("%w: %s", account.ErrEventConflict, event.EventID)
		}
	}

	state, err := snapshotFromTx(tx, event.AccountID)
	if err != nil {
		return account.TransitionResult{}, err
	}
	result, err := account.Apply(state, event)
	if err != nil {
		return account.TransitionResult{}, err
	}
	if result.Idempotent {
		return result, nil
	}

	request, err := requestFromTx(tx, event.RequestID)
	if err != nil {
		return account.TransitionResult{}, err
	}
	if request.AccountID != event.AccountID || request.State != state.Status {
		return account.TransitionResult{}, fmt.Errorf("%w: request %q is %s/%s, account is %s/%s", ErrRequestStateMismatch, request.RequestID, request.AccountID, request.State, state.AccountID, state.Status)
	}
	previousQueueTime := request.NotBefore
	if previousQueueTime.IsZero() {
		previousQueueTime = request.CreatedAt
	}
	request.State = result.State.Status
	request.UpdatedAt = event.OccurredAt
	if request.State == account.Queued {
		request.NotBefore = event.OccurredAt
	} else {
		request.NotBefore = time.Time{}
	}
	if err := request.Validate(); err != nil {
		return account.TransitionResult{}, fmt.Errorf("%w: updated request: %v", ErrCorruptData, err)
	}
	if err := putRequest(tx.Bucket([]byte(migrations.RequestsBucket)), request); err != nil {
		return account.TransitionResult{}, err
	}
	if err := updateQueueIndex(tx, state.Status, result.State.Status, previousQueueTime, request); err != nil {
		return account.TransitionResult{}, err
	}
	if err := putAudit(tx, result.Audit); err != nil {
		return account.TransitionResult{}, err
	}
	if err := putAccountProjection(tx.Bucket([]byte(migrations.AccountsBucket)), result.State); err != nil {
		return account.TransitionResult{}, err
	}
	if err := enqueueNotificationTx(tx, result.Audit.Event, request); err != nil {
		return account.TransitionResult{}, err
	}
	return result, nil
}

// GetAudits returns validated audit history in revision order.
func (s *Store) GetAudits(accountID string) ([]account.AuditRecord, error) {
	var result []account.AuditRecord
	err := s.view(func(tx *bbolt.Tx) error {
		state, err := snapshotFromTx(tx, accountID)
		if err != nil {
			return err
		}
		result = make([]account.AuditRecord, 0, len(state.AppliedEvents))
		for _, audit := range state.AppliedEvents {
			result = append(result, audit)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Revision < result[j].Revision })
		return nil
	})
	return result, err
}

// GetAudit returns one immutable audit record by account and revision.
func (s *Store) GetAudit(accountID string, revision uint64) (account.AuditRecord, error) {
	var result account.AuditRecord
	err := s.view(func(tx *bbolt.Tx) error {
		if _, err := snapshotFromTx(tx, accountID); err != nil {
			return err
		}
		bucket := tx.Bucket([]byte(migrations.AuditsBucket)).Bucket([]byte(accountID))
		if bucket == nil {
			return ErrAuditNotFound
		}
		raw := bucket.Get(revisionKey(revision))
		if raw == nil {
			return ErrAuditNotFound
		}
		return decode(raw, &result)
	})
	return result, err
}

// CreateRequest persists a request exactly once by idempotency key. The
// returned boolean is true when an existing identical request was returned.
func (s *Store) CreateRequest(request Request) (Request, bool, error) {
	if err := request.Validate(); err != nil {
		return Request{}, false, err
	}
	var result Request
	var idempotent bool
	err := s.update(func(tx *bbolt.Tx) error {
		var err error
		result, idempotent, err = createRequestTx(tx, request)
		return err
	})
	return result, idempotent, err
}

// SubmitRequest creates a request and applies its initial NO_REQUEST -> QUEUED
// event in one transaction. A repeated idempotency key returns the existing
// projection without emitting a second transition.
func (s *Store) SubmitRequest(request Request, event account.Event) (Request, bool, error) {
	if err := request.Validate(); err != nil {
		return Request{}, false, err
	}
	if err := event.Validate(); err != nil {
		return Request{}, false, err
	}
	if request.State != account.NoRequest ||
		event.AccountID != request.AccountID || event.RequestID != request.RequestID ||
		event.From != account.NoRequest || event.To != account.Queued {
		return Request{}, false, fmt.Errorf("%w: submit event must be NO_REQUEST -> QUEUED for the request", ErrInvalidRequest)
	}
	var result Request
	var idempotent bool
	err := s.update(func(tx *bbolt.Tx) error {
		existing, alreadyExists, err := submitRequestTx(tx, request)
		if err != nil {
			return err
		}
		idempotent = alreadyExists
		// A previously submitted request may already be queued or terminal. The
		// durable projection is the idempotent result; never replay a new event.
		if alreadyExists && existing.State != account.NoRequest {
			result = existing
			return nil
		}
		if _, err := applyEventTx(tx, event); err != nil {
			return err
		}
		result, err = requestFromTx(tx, request.RequestID)
		return err
	})
	if err != nil {
		return Request{}, false, err
	}
	return result, idempotent, nil
}

func submitRequestTx(tx *bbolt.Tx, request Request) (Request, bool, error) {
	idempotency := tx.Bucket([]byte(migrations.RequestIdempotencyBucket))
	if existingID := idempotency.Get([]byte(request.IdempotencyKey)); existingID != nil {
		existing, err := requestFromTx(tx, string(existingID))
		if err != nil {
			return Request{}, false, err
		}
		if !requestSubmissionEqual(existing, request) {
			return Request{}, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	return createRequestTx(tx, request)
}

func createRequestTx(tx *bbolt.Tx, request Request) (Request, bool, error) {
	requests := tx.Bucket([]byte(migrations.RequestsBucket))
	idempotency := tx.Bucket([]byte(migrations.RequestIdempotencyBucket))
	if existingID := idempotency.Get([]byte(request.IdempotencyKey)); existingID != nil {
		existing, err := requestFromTx(tx, string(existingID))
		if err != nil {
			return Request{}, false, err
		}
		if !requestIdentityEqual(existing, request) {
			return Request{}, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	if request.State != account.NoRequest {
		return Request{}, false, fmt.Errorf("%w: new request must start at NO_REQUEST", ErrInvalidRequest)
	}

	if existingRaw := requests.Get([]byte(request.RequestID)); existingRaw != nil {
		var existing Request
		if err := decode(existingRaw, &existing); err != nil {
			return Request{}, false, err
		}
		if requestIdentityEqual(existing, request) {
			return Request{}, false, fmt.Errorf("%w: idempotency index is missing", ErrCorruptData)
		}
		return Request{}, false, ErrRequestConflict
	}
	state, err := snapshotFromTx(tx, request.AccountID)
	if err != nil {
		return Request{}, false, err
	}
	if state.Status != account.NoRequest {
		return Request{}, false, ErrAccountBusy
	}
	if err := putRequest(requests, request); err != nil {
		return Request{}, false, err
	}
	if err := idempotency.Put([]byte(request.IdempotencyKey), []byte(request.RequestID)); err != nil {
		return Request{}, false, fmt.Errorf("write request idempotency index: %w", err)
	}
	return request, false, nil
}

// ListRequests returns every validated request in deterministic creation order.
func (s *Store) ListRequests() ([]Request, error) {
	var result []Request
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.RequestsBucket)).ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var request Request
			if err := decode(raw, &request); err != nil {
				return err
			}
			if request.State == account.Queued && request.NotBefore.IsZero() {
				request.NotBefore = request.CreatedAt
			}
			if err := request.Validate(); err != nil {
				return fmt.Errorf("%w: invalid request %q: %v", ErrCorruptData, request.RequestID, err)
			}
			result = append(result, request)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].RequestID < result[j].RequestID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// ListQueuedRequests returns ready queued requests in deterministic FIFO order.
// Delayed retries and requests whose deadlines have elapsed are omitted.
func (s *Store) ListQueuedRequests(now time.Time) ([]Request, error) {
	if now.IsZero() {
		return nil, ErrInvalidQueueOptions
	}
	var result []Request
	err := s.view(func(tx *bbolt.Tx) error {
		queued, err := queuedRequestsTx(tx)
		if err != nil {
			return err
		}
		for _, request := range queued {
			if request.NotBefore.After(now) ||
				(!request.Deadline.IsZero() && !now.Before(request.Deadline)) {
				continue
			}
			result = append(result, request)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// LeaseRecord associates a persisted lease with its account.
type LeaseRecord struct {
	AccountID string
	Lease     account.Lease
}

// ListLeases returns validated persisted leases in account ID order.
func (s *Store) ListLeases() ([]LeaseRecord, error) {
	var result []LeaseRecord
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.LeasesBucket)).ForEach(func(key, raw []byte) error {
			if raw == nil {
				return nil
			}
			var lease account.Lease
			if err := decode(raw, &lease); err != nil {
				return err
			}
			if err := lease.Validate(); err != nil {
				return fmt.Errorf("%w: invalid lease: %v", ErrCorruptData, err)
			}
			result = append(result, LeaseRecord{AccountID: string(key), Lease: lease})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AccountID < result[j].AccountID })
	return result, nil
}

// ClaimNext atomically selects the earliest ready request, obtains its account
// lease, and applies QUEUED -> STARTING. Expired leases do not count toward the
// global limit and can be recovered by this transaction.
func (s *Store) ClaimNext(now time.Time, leaseID, owner string, ttl time.Duration, eventID, actor, reason string, options QueueOptions) (Claim, error) {
	if now.IsZero() || strings.TrimSpace(leaseID) == "" || strings.TrimSpace(owner) == "" ||
		ttl <= 0 || strings.TrimSpace(eventID) == "" || strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return Claim{}, ErrInvalidQueueOptions
	}
	if options.MaxGlobalConcurrency < 1 {
		return Claim{}, ErrInvalidQueueOptions
	}
	var result Claim
	err := s.update(func(tx *bbolt.Tx) error {
		if existing, found, err := existingClaimTx(tx, now, eventID, leaseID, owner, actor, reason); err != nil {
			return err
		} else if found {
			result = existing
			return nil
		}
		leases := tx.Bucket([]byte(migrations.LeasesBucket))
		activeLeases, err := activeLeaseCountTx(tx, now)
		if err != nil {
			return err
		}
		if activeLeases >= options.MaxGlobalConcurrency {
			return ErrQueueCapacity
		}

		queued, err := queuedRequestsTx(tx)
		if err != nil {
			return err
		}
		candidates := make([]Request, 0, len(queued))
		for _, request := range queued {
			if !request.NotBefore.After(now) &&
				(request.Deadline.IsZero() || now.Before(request.Deadline)) {
				candidates = append(candidates, request)
			}
		}
		if len(candidates) == 0 {
			return ErrQueueEmpty
		}

		for _, candidate := range candidates {
			current, exists, err := leaseFromTx(tx, candidate.AccountID)
			if err != nil {
				return err
			}
			if exists && !current.Expired(now) {
				continue
			}
			state, err := snapshotFromTx(tx, candidate.AccountID)
			if err != nil {
				return err
			}
			if state.Status != account.Queued || state.RequestID != candidate.RequestID {
				return fmt.Errorf("%w: queued request %q does not match account state", ErrRequestStateMismatch, candidate.RequestID)
			}
			var currentPtr *account.Lease
			if exists {
				currentPtr = &current
			}
			lease, err := account.AcquireLease(currentPtr, now, leaseID, owner, ttl)
			if err != nil {
				if errors.Is(err, account.ErrLeaseHeld) {
					continue
				}
				return err
			}
			event := account.Event{
				EventID:          eventID,
				AccountID:        candidate.AccountID,
				RequestID:        candidate.RequestID,
				From:             account.Queued,
				ExpectedRevision: state.Revision,
				To:               account.Starting,
				Reason:           reason,
				Actor:            actor,
				OccurredAt:       now,
			}
			transition, err := applyEventTx(tx, event)
			if err != nil {
				return err
			}
			updated, err := requestFromTx(tx, candidate.RequestID)
			if err != nil {
				return err
			}
			if updated.Attempt == int(^uint(0)>>1) {
				return ErrRequestAttemptOverflow
			}
			updated.Attempt++
			updated.NotBefore = time.Time{}
			updated.UpdatedAt = now
			if err := updated.Validate(); err != nil {
				return fmt.Errorf("%w: claimed request: %v", ErrCorruptData, err)
			}
			if err := putRequest(tx.Bucket([]byte(migrations.RequestsBucket)), updated); err != nil {
				return err
			}
			if err := putLease(leases, candidate.AccountID, lease); err != nil {
				return err
			}
			result = Claim{Request: updated, Lease: lease, Transition: transition}
			return nil
		}
		return ErrQueueEmpty
	})
	if err != nil {
		return Claim{}, err
	}
	return result, nil
}

func existingClaimTx(tx *bbolt.Tx, now time.Time, eventID, leaseID, owner, actor, reason string) (Claim, bool, error) {
	raw := tx.Bucket([]byte(migrations.EventsBucket)).Get([]byte(eventID))
	if raw == nil {
		return Claim{}, false, nil
	}
	var audit account.AuditRecord
	if err := decode(raw, &audit); err != nil {
		return Claim{}, false, err
	}
	if audit.Event.From != account.Queued || audit.Event.To != account.Starting ||
		audit.Event.Actor != actor || audit.Event.Reason != reason {
		return Claim{}, false, fmt.Errorf("%w: claim event %q is already used", account.ErrEventConflict, eventID)
	}
	request, err := requestFromTx(tx, audit.Event.RequestID)
	if err != nil {
		return Claim{}, false, err
	}
	state, err := snapshotFromTx(tx, audit.Event.AccountID)
	if err != nil {
		return Claim{}, false, err
	}
	lease, exists, err := leaseFromTx(tx, audit.Event.AccountID)
	if err != nil {
		return Claim{}, false, err
	}
	if !exists || lease.LeaseID != leaseID || lease.Owner != owner ||
		request.State != account.Starting || state.Status != account.Starting || state.RequestID != request.RequestID {
		return Claim{}, false, fmt.Errorf("%w: claim event %q no longer owns its request", account.ErrEventConflict, eventID)
	}
	if lease.Expired(now) {
		return Claim{}, false, account.ErrLeaseExpired
	}
	return Claim{
		Request: request,
		Lease:   lease,
		Transition: account.TransitionResult{
			State:      state,
			Audit:      audit,
			Idempotent: true,
		},
	}, true, nil
}

// CancelRequest applies a cancellation event and releases any account lease
// in the same transaction.
func (s *Store) CancelRequest(event account.Event) (account.TransitionResult, error) {
	return s.cancelRequest(event, nil, false)
}

// CancelRequestOwned applies a cancellation event only when the caller still
// owns the exact lease snapshot returned by ClaimNext. Recovery uses this form
// for expired STARTING leases so a concurrent replacement cannot be cancelled.
func (s *Store) CancelRequestOwned(event account.Event, lease account.Lease, allowExpired bool) (account.TransitionResult, error) {
	return s.cancelRequest(event, &lease, allowExpired)
}

func (s *Store) cancelRequest(event account.Event, expectedLease *account.Lease, allowExpired bool) (account.TransitionResult, error) {
	if event.To != account.Cancelled {
		return account.TransitionResult{}, fmt.Errorf("%w: cancellation target %s", ErrInvalidRequest, event.To)
	}
	var result account.TransitionResult
	err := s.update(func(tx *bbolt.Tx) error {
		var err error
		result, err = applyEventTx(tx, event)
		if err != nil {
			return err
		}
		if result.Idempotent {
			return nil
		}
		if err := verifyExpectedLease(tx, event.AccountID, event.OccurredAt, expectedLease, allowExpired, allowExpired); err != nil {
			return err
		}
		return tx.Bucket([]byte(migrations.LeasesBucket)).Delete([]byte(event.AccountID))
	})
	if err != nil {
		return account.TransitionResult{}, err
	}
	return result, nil
}

// CompleteRequest applies a successful terminal session event and releases
// the request lease atomically.
func (s *Store) CompleteRequest(event account.Event) (account.TransitionResult, error) {
	return s.completeRequest(event, nil, false)
}

// CompleteRequestOwned applies a terminal session event only when the caller
// still owns the lease identity returned by ClaimNext. Heartbeats may update
// its timestamps; an expired or replaced worker lease is rejected.
func (s *Store) CompleteRequestOwned(event account.Event, lease account.Lease) (account.TransitionResult, error) {
	return s.completeRequest(event, &lease, false)
}

func (s *Store) completeRequest(event account.Event, expectedLease *account.Lease, allowExpired bool) (account.TransitionResult, error) {
	if event.To != account.LoginSucceeded && event.To != account.Blocked {
		return account.TransitionResult{}, fmt.Errorf("%w: completion target %s", ErrInvalidRequest, event.To)
	}
	var result account.TransitionResult
	err := s.update(func(tx *bbolt.Tx) error {
		var err error
		result, err = applyEventTx(tx, event)
		if err != nil {
			return err
		}
		if result.Idempotent {
			return nil
		}
		if err := verifyExpectedLease(tx, event.AccountID, event.OccurredAt, expectedLease, allowExpired, false); err != nil {
			return err
		}
		return tx.Bucket([]byte(migrations.LeasesBucket)).Delete([]byte(event.AccountID))
	})
	if err != nil {
		return account.TransitionResult{}, err
	}
	return result, nil
}

// FailureResult captures the failure event and optional immediate requeue.
type FailureResult struct {
	Failed  account.TransitionResult
	Retried *account.TransitionResult
	Request Request
}

// RecordFailure records a failed login attempt, releases its lease, and when
// retryEvent is non-nil applies LOGIN_FAILED -> QUEUED in the same transaction.
func (s *Store) RecordFailure(event account.Event, class account.FailureClass, nextAttemptAt time.Time, retryEvent *account.Event) (FailureResult, error) {
	return s.recordFailure(event, class, nextAttemptAt, retryEvent, nil, false)
}

// RecordFailureOwned records a runner failure only when the caller still owns
// the lease identity returned by ClaimNext. allowExpired is used exclusively
// by restart recovery, which intentionally consumes an unchanged expired
// lease snapshot.
func (s *Store) RecordFailureOwned(event account.Event, class account.FailureClass, nextAttemptAt time.Time, retryEvent *account.Event, lease account.Lease, allowExpired bool) (FailureResult, error) {
	return s.recordFailure(event, class, nextAttemptAt, retryEvent, &lease, allowExpired)
}

func (s *Store) recordFailure(event account.Event, class account.FailureClass, nextAttemptAt time.Time, retryEvent *account.Event, expectedLease *account.Lease, allowExpired bool) (FailureResult, error) {
	if !validFailureClass(class) || event.To != account.LoginFailed {
		return FailureResult{}, fmt.Errorf("%w: invalid failure", ErrInvalidRequest)
	}
	if retryEvent != nil && nextAttemptAt.IsZero() {
		return FailureResult{}, fmt.Errorf("%w: retry time is required", ErrInvalidRequest)
	}
	if retryEvent != nil && nextAttemptAt.Before(retryEvent.OccurredAt) {
		return FailureResult{}, fmt.Errorf("%w: retry time precedes retry event", ErrInvalidRequest)
	}
	var result FailureResult
	err := s.update(func(tx *bbolt.Tx) error {
		var err error
		result.Failed, err = applyEventTx(tx, event)
		if err != nil {
			return err
		}
		if result.Failed.Idempotent {
			result.Request, err = requestFromTx(tx, event.RequestID)
			if err != nil || retryEvent == nil {
				return err
			}
			// A duplicate compound operation must report an already-persisted
			// retry without applying it again. If the original failure did not
			// include a retry, leave Retried nil and preserve idempotency.
			if raw := tx.Bucket([]byte(migrations.EventsBucket)).Get([]byte(retryEvent.EventID)); raw == nil {
				return nil
			}
			var retried account.TransitionResult
			retried, err = applyEventTx(tx, *retryEvent)
			if err != nil {
				return err
			}
			result.Retried = &retried
			return nil
		}
		if err := verifyExpectedLease(tx, event.AccountID, event.OccurredAt, expectedLease, allowExpired, allowExpired); err != nil {
			return err
		}
		if err := tx.Bucket([]byte(migrations.LeasesBucket)).Delete([]byte(event.AccountID)); err != nil {
			return err
		}
		request, err := requestFromTx(tx, event.RequestID)
		if err != nil {
			return err
		}
		request.LastFailure = class
		request.UpdatedAt = event.OccurredAt
		if retryEvent != nil {
			if retryEvent.AccountID != event.AccountID || retryEvent.RequestID != event.RequestID ||
				retryEvent.From != account.LoginFailed || retryEvent.To != account.Queued ||
				retryEvent.ExpectedRevision != result.Failed.State.Revision {
				return fmt.Errorf("%w: retry event does not follow failure", ErrInvalidRequest)
			}
			var retryResult account.TransitionResult
			retryResult, err = applyEventTx(tx, *retryEvent)
			if err != nil {
				return err
			}
			result.Retried = &retryResult
			request, err = requestFromTx(tx, event.RequestID)
			if err != nil {
				return err
			}
			request.LastFailure = class
			request.NotBefore = nextAttemptAt
			request.UpdatedAt = retryEvent.OccurredAt
		}
		if err := request.Validate(); err != nil {
			return fmt.Errorf("%w: failed request: %v", ErrCorruptData, err)
		}
		if err := putRequest(tx.Bucket([]byte(migrations.RequestsBucket)), request); err != nil {
			return err
		}
		if err := setNotificationFailureTx(tx, event.EventID, class); err != nil {
			return err
		}
		result.Request = request
		return nil
	})
	if err != nil {
		return FailureResult{}, err
	}
	return result, nil
}

// GetRequest returns a validated request projection.
func (s *Store) GetRequest(requestID string) (Request, error) {
	var result Request
	err := s.view(func(tx *bbolt.Tx) error {
		var err error
		result, err = requestFromTx(tx, requestID)
		return err
	})
	return result, err
}

// GetLease returns a lease and whether one exists for the account.
func (s *Store) GetLease(accountID string) (account.Lease, bool, error) {
	var result account.Lease
	var exists bool
	err := s.view(func(tx *bbolt.Tx) error {
		if _, err := snapshotFromTx(tx, accountID); err != nil {
			return err
		}
		lease, ok, err := leaseFromTx(tx, accountID)
		result, exists = lease, ok
		return err
	})
	return result, exists, err
}

// AcquireLease atomically obtains or recovers the account lease.
func (s *Store) AcquireLease(accountID string, now time.Time, leaseID, owner string, ttl time.Duration) (account.Lease, error) {
	var result account.Lease
	err := s.update(func(tx *bbolt.Tx) error {
		if _, err := snapshotFromTx(tx, accountID); err != nil {
			return err
		}
		current, exists, err := leaseFromTx(tx, accountID)
		if err != nil {
			return err
		}
		var currentPtr *account.Lease
		if exists {
			currentPtr = &current
		}
		result, err = account.AcquireLease(currentPtr, now, leaseID, owner, ttl)
		if err != nil {
			return err
		}
		return putLease(tx.Bucket([]byte(migrations.LeasesBucket)), accountID, result)
	})
	return result, err
}

// HeartbeatLease atomically extends the owned, unexpired account lease.
func (s *Store) HeartbeatLease(accountID string, now time.Time, leaseID, owner string, ttl time.Duration) (account.Lease, error) {
	var result account.Lease
	err := s.update(func(tx *bbolt.Tx) error {
		if _, err := snapshotFromTx(tx, accountID); err != nil {
			return err
		}
		current, exists, err := leaseFromTx(tx, accountID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrLeaseNotFound
		}
		result, err = account.HeartbeatLease(current, now, leaseID, owner, ttl)
		if err != nil {
			return err
		}
		return putLease(tx.Bucket([]byte(migrations.LeasesBucket)), accountID, result)
	})
	return result, err
}

// ReleaseLease atomically clears an owned account lease.
func (s *Store) ReleaseLease(accountID, leaseID, owner string) error {
	return s.update(func(tx *bbolt.Tx) error {
		if _, err := snapshotFromTx(tx, accountID); err != nil {
			return err
		}
		current, exists, err := leaseFromTx(tx, accountID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrLeaseNotFound
		}
		if _, err := account.ReleaseLease(current, leaseID, owner); err != nil {
			return err
		}
		return tx.Bucket([]byte(migrations.LeasesBucket)).Delete([]byte(accountID))
	})
}

// Backup creates a consistent, timestamped snapshot in the configured backup
// directory. The destination path is always derived from Config.BackupPath.
func (s *Store) Backup(at time.Time) (string, error) {
	if s == nil || s.db == nil {
		return "", bbolt.ErrDatabaseNotOpen
	}
	path, err := s.cfg.BackupPath(at)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.cfg.BackupDir(), 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return "", ErrBackupExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect backup path: %w", err)
	}
	err = s.db.View(func(tx *bbolt.Tx) error {
		return tx.CopyFile(path, 0o600)
	})
	if err != nil {
		return "", fmt.Errorf("copy backup: %w", err)
	}
	return path, nil
}

func (s *Store) view(fn func(*bbolt.Tx) error) error {
	if s == nil || s.db == nil {
		return bbolt.ErrDatabaseNotOpen
	}
	return s.db.View(fn)
}

func (s *Store) update(fn func(*bbolt.Tx) error) error {
	if s == nil || s.db == nil {
		return bbolt.ErrDatabaseNotOpen
	}
	return s.db.Update(fn)
}

type accountProjection struct {
	AccountID string         `json:"account_id"`
	Status    account.Status `json:"status"`
	RequestID string         `json:"request_id,omitempty"`
	Revision  uint64         `json:"revision"`
}

func snapshotFromTx(tx *bbolt.Tx, accountID string) (account.Snapshot, error) {
	if strings.TrimSpace(accountID) == "" {
		return account.Snapshot{}, ErrInvalidAccount
	}
	accounts := tx.Bucket([]byte(migrations.AccountsBucket))
	raw := accounts.Get([]byte(accountID))
	if raw == nil {
		return account.Snapshot{}, ErrAccountNotFound
	}
	var projection accountProjection
	if err := decode(raw, &projection); err != nil {
		return account.Snapshot{}, err
	}
	if projection.AccountID != accountID {
		return account.Snapshot{}, fmt.Errorf("%w: account projection key mismatch", ErrCorruptData)
	}
	if !projection.Status.Valid() {
		return account.Snapshot{}, fmt.Errorf("%w: invalid account status", ErrCorruptData)
	}

	applied := make(map[string]account.AuditRecord)
	auditRoot := tx.Bucket([]byte(migrations.AuditsBucket))
	auditBucket := auditRoot.Bucket([]byte(accountID))
	if projection.Revision > 0 && auditBucket == nil {
		return account.Snapshot{}, fmt.Errorf("%w: account has no audit bucket", ErrCorruptData)
	}
	if auditBucket != nil {
		err := auditBucket.ForEach(func(key, value []byte) error {
			if value == nil || len(key) != 8 {
				return fmt.Errorf("%w: invalid audit entry", ErrCorruptData)
			}
			var audit account.AuditRecord
			if err := decode(value, &audit); err != nil {
				return err
			}
			if audit.Revision != binary.BigEndian.Uint64(key) {
				return fmt.Errorf("%w: audit revision key mismatch", ErrCorruptData)
			}
			if _, exists := applied[audit.Event.EventID]; exists {
				return fmt.Errorf("%w: duplicate audit event id", ErrCorruptData)
			}
			eventRaw := tx.Bucket([]byte(migrations.EventsBucket)).Get([]byte(audit.Event.EventID))
			if eventRaw == nil {
				return fmt.Errorf("%w: missing event index", ErrCorruptData)
			}
			var indexed account.AuditRecord
			if err := decode(eventRaw, &indexed); err != nil {
				return err
			}
			if !auditRecordsEqual(audit, indexed) {
				return fmt.Errorf("%w: event index mismatch", ErrCorruptData)
			}
			applied[audit.Event.EventID] = audit
			return nil
		})
		if err != nil {
			return account.Snapshot{}, err
		}
	}
	snapshot := account.Snapshot{
		AccountID:     projection.AccountID,
		Status:        projection.Status,
		RequestID:     projection.RequestID,
		Revision:      projection.Revision,
		AppliedEvents: applied,
	}
	if err := snapshot.Validate(); err != nil {
		return account.Snapshot{}, fmt.Errorf("%w: invalid account snapshot: %v", ErrCorruptData, err)
	}
	return snapshot, nil
}

func putAccountProjection(bucket *bbolt.Bucket, snapshot account.Snapshot) error {
	projection := accountProjection{
		AccountID: snapshot.AccountID,
		Status:    snapshot.Status,
		RequestID: snapshot.RequestID,
		Revision:  snapshot.Revision,
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return fmt.Errorf("encode account projection: %w", err)
	}
	if err := bucket.Put([]byte(snapshot.AccountID), raw); err != nil {
		return fmt.Errorf("write account projection: %w", err)
	}
	return nil
}

func requestFromTx(tx *bbolt.Tx, requestID string) (Request, error) {
	if strings.TrimSpace(requestID) == "" {
		return Request{}, ErrRequestNotFound
	}
	raw := tx.Bucket([]byte(migrations.RequestsBucket)).Get([]byte(requestID))
	if raw == nil {
		return Request{}, ErrRequestNotFound
	}
	var request Request
	if err := decode(raw, &request); err != nil {
		return Request{}, err
	}
	// v1 requests did not persist scheduling metadata. Their creation time is
	// the only deterministic FIFO timestamp available during migration.
	if request.State == account.Queued && request.NotBefore.IsZero() {
		request.NotBefore = request.CreatedAt
	}
	if err := request.Validate(); err != nil {
		return Request{}, fmt.Errorf("%w: invalid request %q: %v", ErrCorruptData, requestID, err)
	}
	return request, nil
}

func putRequest(bucket *bbolt.Bucket, request Request) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	if err := bucket.Put([]byte(request.RequestID), raw); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	return nil
}

func queuedRequestsTx(tx *bbolt.Tx) ([]Request, error) {
	requests := tx.Bucket([]byte(migrations.RequestsBucket))
	if requests == nil {
		return nil, fmt.Errorf("%w: requests bucket is missing", ErrCorruptData)
	}
	result := make([]Request, 0)
	err := requests.ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		var request Request
		if err := decode(value, &request); err != nil {
			return err
		}
		if request.State != account.Queued {
			return nil
		}
		if request.NotBefore.IsZero() {
			request.NotBefore = request.CreatedAt
		}
		if err := request.Validate(); err != nil {
			return fmt.Errorf("%w: invalid queued request %q: %v", ErrCorruptData, string(key), err)
		}
		result = append(result, request)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.RequestID < right.RequestID
	})
	return result, nil
}

func activeLeaseCountTx(tx *bbolt.Tx, now time.Time) (int, error) {
	leases := tx.Bucket([]byte(migrations.LeasesBucket))
	if leases == nil {
		return 0, fmt.Errorf("%w: leases bucket is missing", ErrCorruptData)
	}
	count := 0
	err := leases.ForEach(func(key, value []byte) error {
		if value == nil {
			return nil
		}
		var lease account.Lease
		if err := decode(value, &lease); err != nil {
			return err
		}
		if err := lease.Validate(); err != nil {
			return fmt.Errorf("%w: invalid lease %q: %v", ErrCorruptData, string(key), err)
		}
		if !lease.Expired(now) {
			count++
		}
		return nil
	})
	return count, err
}

func queueKey(at time.Time, requestID string) []byte {
	// Flip the sign bit so lexicographic order matches signed UnixNano order,
	// then append the request ID to make equal timestamps deterministic.
	key := make([]byte, 9+len(requestID))
	binary.BigEndian.PutUint64(key[:8], uint64(at.UTC().UnixNano())^(uint64(1)<<63))
	key[8] = 0
	copy(key[9:], requestID)
	return key
}

func putQueueEntry(tx *bbolt.Tx, request Request) error {
	queue := tx.Bucket([]byte(migrations.QueueBucket))
	if queue == nil {
		return fmt.Errorf("%w: queue bucket is missing", ErrCorruptData)
	}
	at := request.CreatedAt
	if at.IsZero() || strings.TrimSpace(request.RequestID) == "" {
		return fmt.Errorf("%w: queue entry has no timestamp or request id", ErrCorruptData)
	}
	return queue.Put(queueKey(at, request.RequestID), []byte(request.RequestID))
}

func deleteQueueEntry(tx *bbolt.Tx, createdAt time.Time, requestID string) error {
	queue := tx.Bucket([]byte(migrations.QueueBucket))
	if queue == nil {
		return fmt.Errorf("%w: queue bucket is missing", ErrCorruptData)
	}
	if createdAt.IsZero() {
		return nil
	}
	return queue.Delete(queueKey(createdAt, requestID))
}

func updateQueueIndex(tx *bbolt.Tx, from, to account.Status, _ time.Time, request Request) error {
	if from == account.Queued {
		if err := deleteQueueEntry(tx, request.CreatedAt, request.RequestID); err != nil {
			return fmt.Errorf("remove queue entry: %w", err)
		}
	}
	if to == account.Queued {
		if err := putQueueEntry(tx, request); err != nil {
			return fmt.Errorf("write queue entry: %w", err)
		}
	}
	return nil
}

func rebuildQueueIndex(db *bbolt.DB) error {
	return db.Update(func(tx *bbolt.Tx) error {
		queue := tx.Bucket([]byte(migrations.QueueBucket))
		requests := tx.Bucket([]byte(migrations.RequestsBucket))
		if queue == nil || requests == nil {
			return fmt.Errorf("%w: queue schema buckets are missing", ErrCorruptData)
		}
		keys := make([][]byte, 0)
		if err := queue.ForEach(func(key, value []byte) error {
			if value != nil {
				keys = append(keys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range keys {
			if err := queue.Delete(key); err != nil {
				return err
			}
		}
		type queuedRequest struct {
			request      Request
			needsPersist bool
		}
		queuedRequests := make([]queuedRequest, 0)
		if err := requests.ForEach(func(key, value []byte) error {
			if value == nil {
				return nil
			}
			var request Request
			if err := decode(value, &request); err != nil {
				return err
			}
			if request.State != account.Queued {
				return nil
			}
			needsPersist := false
			if request.NotBefore.IsZero() {
				request.NotBefore = request.CreatedAt
				needsPersist = true
			}
			if err := request.Validate(); err != nil {
				return fmt.Errorf("%w: invalid queued request %q: %v", ErrCorruptData, string(key), err)
			}
			queuedRequests = append(queuedRequests, queuedRequest{request: request, needsPersist: needsPersist})
			return nil
		}); err != nil {
			return err
		}
		for _, queued := range queuedRequests {
			if queued.needsPersist {
				if err := putRequest(requests, queued.request); err != nil {
					return err
				}
			}
			if err := putQueueEntry(tx, queued.request); err != nil {
				return err
			}
		}
		return nil
	})
}

func putAudit(tx *bbolt.Tx, audit account.AuditRecord) error {
	root := tx.Bucket([]byte(migrations.AuditsBucket))
	bucket, err := root.CreateBucketIfNotExists([]byte(audit.Event.AccountID))
	if err != nil {
		return fmt.Errorf("create audit account bucket: %w", err)
	}
	key := revisionKey(audit.Revision)
	if bucket.Get(key) != nil {
		return fmt.Errorf("%w: audit revision %d already exists", ErrCorruptData, audit.Revision)
	}
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode audit: %w", err)
	}
	if err := bucket.Put(key, raw); err != nil {
		return fmt.Errorf("write audit: %w", err)
	}
	events := tx.Bucket([]byte(migrations.EventsBucket))
	if events.Get([]byte(audit.Event.EventID)) != nil {
		return fmt.Errorf("%w: event %s already exists", account.ErrEventConflict, audit.Event.EventID)
	}
	if err := events.Put([]byte(audit.Event.EventID), raw); err != nil {
		return fmt.Errorf("write event index: %w", err)
	}
	return nil
}

func leaseFromTx(tx *bbolt.Tx, accountID string) (account.Lease, bool, error) {
	raw := tx.Bucket([]byte(migrations.LeasesBucket)).Get([]byte(accountID))
	if raw == nil {
		return account.Lease{}, false, nil
	}
	var lease account.Lease
	if err := decode(raw, &lease); err != nil {
		return account.Lease{}, false, err
	}
	if err := lease.Validate(); err != nil {
		return account.Lease{}, false, fmt.Errorf("%w: invalid lease: %v", ErrCorruptData, err)
	}
	return lease, true, nil
}

func verifyExpectedLease(tx *bbolt.Tx, accountID string, at time.Time, expected *account.Lease, allowExpired bool, requireExact bool) error {
	if expected == nil {
		return nil
	}
	current, exists, err := leaseFromTx(tx, accountID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrLeaseNotFound
	}
	if current.LeaseID != expected.LeaseID || current.Owner != expected.Owner {
		return account.ErrLeaseNotOwned
	}
	if requireExact && !leasesEqual(current, *expected) {
		return account.ErrLeaseNotOwned
	}
	if !allowExpired && current.Expired(at) {
		return account.ErrLeaseExpired
	}
	return nil
}

func leasesEqual(left, right account.Lease) bool {
	return left.LeaseID == right.LeaseID &&
		left.Owner == right.Owner &&
		left.AcquiredAt.Equal(right.AcquiredAt) &&
		left.LastHeartbeat.Equal(right.LastHeartbeat) &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

func putLease(bucket *bbolt.Bucket, accountID string, lease account.Lease) error {
	raw, err := json.Marshal(lease)
	if err != nil {
		return fmt.Errorf("encode lease: %w", err)
	}
	if err := bucket.Put([]byte(accountID), raw); err != nil {
		return fmt.Errorf("write lease: %w", err)
	}
	return nil
}

func revisionKey(revision uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, revision)
	return key
}

func decode(raw []byte, target any) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: decode JSON: %v", ErrCorruptData, err)
	}
	return nil
}

func eventsEqual(left, right account.Event) bool {
	return left.EventID == right.EventID &&
		left.AccountID == right.AccountID &&
		left.RequestID == right.RequestID &&
		left.From == right.From &&
		left.ExpectedRevision == right.ExpectedRevision &&
		left.To == right.To &&
		left.Reason == right.Reason &&
		left.Actor == right.Actor &&
		left.OccurredAt.Equal(right.OccurredAt)
}

func auditRecordsEqual(left, right account.AuditRecord) bool {
	return left.Revision == right.Revision && eventsEqual(left.Event, right.Event)
}

func requestIdentityEqual(left, right Request) bool {
	return left.RequestID == right.RequestID &&
		left.AccountID == right.AccountID &&
		left.IdempotencyKey == right.IdempotencyKey &&
		left.NotificationRoomID == right.NotificationRoomID &&
		left.CreatedAt.Equal(right.CreatedAt) &&
		left.Deadline.Equal(right.Deadline)
}

func requestSubmissionEqual(left, right Request) bool {
	return left.RequestID == right.RequestID &&
		left.AccountID == right.AccountID &&
		left.IdempotencyKey == right.IdempotencyKey &&
		left.NotificationRoomID == right.NotificationRoomID &&
		left.Deadline.Equal(right.Deadline)
}
