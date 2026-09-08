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
	ErrInvalidAccount       = errors.New("store: invalid account")
	ErrAccountExists        = errors.New("store: account already exists")
	ErrAccountNotFound      = errors.New("store: account not found")
	ErrAccountBusy          = errors.New("store: account already has an active lifecycle")
	ErrInvalidRequest       = errors.New("store: invalid request")
	ErrRequestExists        = errors.New("store: request already exists")
	ErrRequestNotFound      = errors.New("store: request not found")
	ErrRequestConflict      = errors.New("store: request conflict")
	ErrIdempotencyConflict  = errors.New("store: idempotency key conflict")
	ErrRequestStateMismatch = errors.New("store: request state mismatch")
	ErrAuditNotFound        = errors.New("store: audit record not found")
	ErrLeaseNotFound        = errors.New("store: lease not found")
	ErrBackupExists         = errors.New("store: backup already exists")
	ErrCorruptData          = errors.New("store: corrupt data")
)

// Request is the durable request projection. State mirrors the account state
// after an accepted event; a newly-created request starts at NO_REQUEST until
// its queue event is applied.
type Request struct {
	RequestID      string         `json:"request_id"`
	AccountID      string         `json:"account_id"`
	IdempotencyKey string         `json:"idempotency_key"`
	State          account.Status `json:"state"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
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
	return nil
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
		if raw := tx.Bucket([]byte(migrations.EventsBucket)).Get([]byte(event.EventID)); raw != nil {
			var previous account.AuditRecord
			if err := decode(raw, &previous); err != nil {
				return err
			}
			if !eventsEqual(previous.Event, event) {
				return fmt.Errorf("%w: %s", account.ErrEventConflict, event.EventID)
			}
		}

		state, err := snapshotFromTx(tx, event.AccountID)
		if err != nil {
			return err
		}
		result, err = account.Apply(state, event)
		if err != nil {
			return err
		}
		if result.Idempotent {
			return nil
		}

		request, err := requestFromTx(tx, event.RequestID)
		if err != nil {
			return err
		}
		if request.AccountID != event.AccountID || request.State != state.Status {
			return fmt.Errorf("%w: request %q is %s/%s, account is %s/%s", ErrRequestStateMismatch, request.RequestID, request.AccountID, request.State, state.AccountID, state.Status)
		}
		request.State = result.State.Status
		request.UpdatedAt = event.OccurredAt
		if err := request.Validate(); err != nil {
			return fmt.Errorf("%w: updated request: %v", ErrCorruptData, err)
		}
		if err := putRequest(tx.Bucket([]byte(migrations.RequestsBucket)), request); err != nil {
			return err
		}
		if err := putAudit(tx, result.Audit); err != nil {
			return err
		}
		return putAccountProjection(tx.Bucket([]byte(migrations.AccountsBucket)), result.State)
	})
	if err != nil {
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
		requests := tx.Bucket([]byte(migrations.RequestsBucket))
		idempotency := tx.Bucket([]byte(migrations.RequestIdempotencyBucket))
		if existingID := idempotency.Get([]byte(request.IdempotencyKey)); existingID != nil {
			existing, err := requestFromTx(tx, string(existingID))
			if err != nil {
				return err
			}
			if !requestIdentityEqual(existing, request) {
				return ErrIdempotencyConflict
			}
			result, idempotent = existing, true
			return nil
		}
		if request.State != account.NoRequest {
			return fmt.Errorf("%w: new request must start at NO_REQUEST", ErrInvalidRequest)
		}

		if existingRaw := requests.Get([]byte(request.RequestID)); existingRaw != nil {
			var existing Request
			if err := decode(existingRaw, &existing); err != nil {
				return err
			}
			if requestIdentityEqual(existing, request) {
				return fmt.Errorf("%w: idempotency index is missing", ErrCorruptData)
			}
			return ErrRequestConflict
		}
		state, err := snapshotFromTx(tx, request.AccountID)
		if err != nil {
			return err
		}
		if state.Status != account.NoRequest {
			return ErrAccountBusy
		}
		if err := putRequest(requests, request); err != nil {
			return err
		}
		if err := idempotency.Put([]byte(request.IdempotencyKey), []byte(request.RequestID)); err != nil {
			return fmt.Errorf("write request idempotency index: %w", err)
		}
		result = request
		return nil
	})
	return result, idempotent, err
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
		left.CreatedAt.Equal(right.CreatedAt)
}
