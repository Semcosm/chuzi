// Package credential provides the least-privilege encrypted credential
// boundary. Plaintext is exposed only for the duration of a caller callback;
// the durable backend stores ciphertext and metadata only.
package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxPayloadSize bounds one credential payload and prevents accidental
	// storage of page contents or other unbounded data.
	MaxPayloadSize = 64 * 1024

	OperationStore  = "store"
	OperationAccess = "access"
	OperationRotate = "rotate"
	OperationRevoke = "revoke"
)

var (
	ErrInvalidService     = errors.New("credential: invalid service")
	ErrInvalidKey         = errors.New("credential: invalid key")
	ErrInvalidCredential  = errors.New("credential: invalid credential")
	ErrInvalidAudit       = errors.New("credential: invalid audit")
	ErrCredentialNotFound = errors.New("credential: not found")
	ErrCredentialRevoked  = errors.New("credential: revoked")
	ErrKeyUnavailable     = errors.New("credential: key unavailable")
	ErrAuthentication     = errors.New("credential: authentication failed")
	ErrSessionInvalidation = errors.New("credential: session invalidation failed")
	ErrAuditConflict       = errors.New("credential: audit conflict")
	ErrVersionConflict     = errors.New("credential: version conflict")
	ErrAlreadyCurrentKey   = errors.New("credential: already uses current key")
)

// Key contains a key identifier and private key material. The material is
// intentionally unexported and cannot be read through the public API.
type Key struct {
	id       string
	material []byte
}

// NewKey validates and copies key material. AES-128, AES-192 and AES-256 are
// accepted; deployments should normally use AES-256.
func NewKey(id string, material []byte) (Key, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id, "key id"); err != nil {
		return Key{}, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	if len(material) != 16 && len(material) != 24 && len(material) != 32 {
		return Key{}, fmt.Errorf("%w: key material must be 16, 24, or 32 bytes", ErrInvalidKey)
	}
	return Key{id: id, material: append([]byte(nil), material...)}, nil
}

// ID returns the non-secret key identifier.
func (k Key) ID() string { return k.id }

func (k Key) validate() error {
	if err := validateIdentifier(k.id, "key id"); err != nil ||
		(len(k.material) != 16 && len(k.material) != 24 && len(k.material) != 32) {
		return ErrInvalidKey
	}
	return nil
}

// Keyring resolves the current encryption key and historical keys needed to
// decrypt records during a key rotation. Implementations must keep key
// material outside the repository and durable database.
type Keyring interface {
	Current(context.Context) (Key, error)
	Lookup(context.Context, string) (Key, error)
}

// Record is the encrypted durable credential representation. It contains no
// plaintext. A revoked record has its nonce and ciphertext wiped.
type Record struct {
	AccountID  string     `json:"account_id"`
	Version    uint64     `json:"version"`
	KeyID      string     `json:"key_id"`
	Nonce      []byte     `json:"nonce,omitempty"`
	Ciphertext []byte     `json:"ciphertext,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Validate checks a record loaded from storage or supplied by a backend.
func (r Record) Validate() error {
	if err := validateIdentifier(r.AccountID, "account id"); err != nil ||
		r.Version == 0 || validateIdentifier(r.KeyID, "key id") != nil ||
		r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrInvalidCredential
	}
	if r.RevokedAt != nil {
		if r.RevokedAt.IsZero() || r.RevokedAt.Before(r.UpdatedAt) ||
			len(r.Nonce) != 0 || len(r.Ciphertext) != 0 {
			return ErrInvalidCredential
		}
		return nil
	}
	if len(r.Nonce) != 12 || len(r.Ciphertext) < 16 || len(r.Ciphertext) > MaxPayloadSize+16 {
		return ErrInvalidCredential
	}
	return nil
}

// Metadata is the safe view returned to callers. It never includes nonce or
// ciphertext.
type Metadata struct {
	AccountID string     `json:"account_id"`
	Version   uint64     `json:"version"`
	KeyID     string     `json:"key_id"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func metadataFromRecord(record Record) Metadata {
	metadata := Metadata{
		AccountID: record.AccountID,
		Version:   record.Version,
		KeyID:     record.KeyID,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
	if record.RevokedAt != nil {
		revokedAt := *record.RevokedAt
		metadata.RevokedAt = &revokedAt
	}
	return metadata
}

// Audit records a credential operation without recording secret material.
type Audit struct {
	AuditID    string    `json:"audit_id"`
	AccountID  string    `json:"account_id"`
	Operation  string    `json:"operation"`
	Actor      string    `json:"actor"`
	Version    uint64    `json:"version"`
	KeyID      string    `json:"key_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (a Audit) Validate() error {
	if err := validateIdentifier(a.AuditID, "audit id"); err != nil ||
		validateIdentifier(a.AccountID, "account id") != nil ||
		validateIdentifier(a.Actor, "actor") != nil ||
		validateIdentifier(a.KeyID, "key id") != nil ||
		a.Version == 0 || a.OccurredAt.IsZero() {
		return ErrInvalidAudit
	}
	switch a.Operation {
	case OperationStore, OperationAccess, OperationRotate, OperationRevoke:
		return nil
	default:
		return ErrInvalidAudit
	}
}

// Mutation atomically applies an optional encrypted record change and its
// audit record. Record is nil for access-only audits.
type Mutation struct {
	AccountID string
	Record    *Record
	Audit     Audit
}

func (m Mutation) Validate() error {
	if validateIdentifier(m.AccountID, "account id") != nil || m.Audit.AccountID != m.AccountID {
		return ErrInvalidCredential
	}
	if err := m.Audit.Validate(); err != nil {
		return err
	}
	if m.Record != nil {
		if err := m.Record.Validate(); err != nil || m.Record.AccountID != m.AccountID {
			return ErrInvalidCredential
		}
	}
	return nil
}

// Backend is implemented by the durable store. ApplyCredentialMutation must
// write the record (when non-nil) and audit atomically, and must treat an
// identical AuditID as idempotent.
type Backend interface {
	GetCredential(accountID string) (Record, bool, error)
	ApplyCredentialMutation(Mutation) error
	ListCredentialAudits(accountID string) ([]Audit, error)
}

// SessionInvalidator is an optional boundary implemented by a session
// manager. Revocation asks it to stop account sessions before durable
// credential material is wiped; a failure aborts revocation.
type SessionInvalidator interface {
	Invalidate(context.Context, string, time.Time) error
}

// Service provides encrypted credential operations over a Backend and
// deployment-owned Keyring.
type Service struct {
	backend    Backend
	keys       Keyring
	invalidator SessionInvalidator
}

// New constructs a credential service. It does not load or persist key
// material until an operation needs it.
func New(backend Backend, keys Keyring) (*Service, error) {
	return NewWithSessionInvalidator(backend, keys, nil)
}

// NewWithSessionInvalidator adds the optional session invalidation boundary
// used when a running browser session must be stopped before revocation.
func NewWithSessionInvalidator(backend Backend, keys Keyring, invalidator SessionInvalidator) (*Service, error) {
	if backend == nil || keys == nil {
		return nil, ErrInvalidService
	}
	return &Service{backend: backend, keys: keys, invalidator: invalidator}, nil
}

// Put stores a new credential version encrypted with the current key. The
// plaintext input is copied and never retained after this call.
func (s *Service) Put(ctx context.Context, accountID string, plaintext []byte, actor string, at time.Time) (Metadata, error) {
	if err := s.validateCall(ctx, accountID, actor, at); err != nil {
		return Metadata{}, err
	}
	if len(plaintext) == 0 || len(plaintext) > MaxPayloadSize {
		return Metadata{}, ErrInvalidCredential
	}
	current, err := s.keys.Current(ctx)
	if err != nil {
		return Metadata{}, normalizeKeyError(err)
	}
	if err := current.validate(); err != nil {
		return Metadata{}, ErrKeyUnavailable
	}
	existing, found, err := s.backend.GetCredential(accountID)
	if err != nil {
		return Metadata{}, err
	}
	version := uint64(1)
	createdAt := at
	if found {
		if err := existing.Validate(); err != nil {
			return Metadata{}, err
		}
		if at.Before(existing.UpdatedAt) {
			return Metadata{}, ErrInvalidCredential
		}
		version = existing.Version + 1
		createdAt = existing.CreatedAt
	}
	working := append([]byte(nil), plaintext...)
	defer clear(working)
	nonce, ciphertext, err := seal(current, accountID, version, working)
	if err != nil {
		return Metadata{}, err
	}
	record := Record{
		AccountID:  accountID,
		Version:    version,
		KeyID:      current.ID(),
		Nonce:      nonce,
		Ciphertext: ciphertext,
		CreatedAt:  createdAt,
		UpdatedAt:  at,
	}
	audit, err := newAudit(accountID, OperationStore, actor, version, current.ID(), at)
	if err != nil {
		return Metadata{}, err
	}
	if err := s.backend.ApplyCredentialMutation(Mutation{AccountID: accountID, Record: &record, Audit: audit}); err != nil {
		return Metadata{}, err
	}
	return metadataFromRecord(record), nil
}

// Set is an explicit alias for Put for provisioning call sites.
func (s *Service) Set(ctx context.Context, accountID string, plaintext []byte, actor string, at time.Time) (Metadata, error) {
	return s.Put(ctx, accountID, plaintext, actor, at)
}

// Use decrypts a credential only for fn and clears the working plaintext after
// fn returns. fn must not retain the supplied slice.
func (s *Service) Use(ctx context.Context, accountID, actor string, at time.Time, fn func([]byte) error) error {
	if fn == nil {
		return ErrInvalidService
	}
	if err := s.validateCall(ctx, accountID, actor, at); err != nil {
		return err
	}
	record, found, err := s.backend.GetCredential(accountID)
	if err != nil {
		return err
	}
	if !found {
		return ErrCredentialNotFound
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if at.Before(record.UpdatedAt) {
		return ErrInvalidCredential
	}
	if record.RevokedAt != nil {
		return ErrCredentialRevoked
	}
	key, err := s.keys.Lookup(ctx, record.KeyID)
	if err != nil {
		return normalizeKeyError(err)
	}
	working, err := open(key, record.AccountID, record.Version, record.Nonce, record.Ciphertext)
	if err != nil {
		return err
	}
	defer clear(working)
	audit, err := newAudit(accountID, OperationAccess, actor, record.Version, record.KeyID, at)
	if err != nil {
		return err
	}
	if err := s.backend.ApplyCredentialMutation(Mutation{AccountID: accountID, Audit: audit}); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return fn(working)
}

// Access is a descriptive alias for Use.
func (s *Service) Access(ctx context.Context, accountID, actor string, at time.Time, fn func([]byte) error) error {
	return s.Use(ctx, accountID, actor, at, fn)
}

// RotateKey decrypts an active record with its historical key and re-encrypts
// it with the keyring's current key. The credential payload never leaves this
// package except through the caller's previous provisioning call.
func (s *Service) RotateKey(ctx context.Context, accountID, actor string, at time.Time) (Metadata, error) {
	if err := s.validateCall(ctx, accountID, actor, at); err != nil {
		return Metadata{}, err
	}
	record, found, err := s.backend.GetCredential(accountID)
	if err != nil {
		return Metadata{}, err
	}
	if !found {
		return Metadata{}, ErrCredentialNotFound
	}
	if err := record.Validate(); err != nil {
		return Metadata{}, err
	}
	if at.Before(record.UpdatedAt) {
		return Metadata{}, ErrInvalidCredential
	}
	if record.RevokedAt != nil {
		return Metadata{}, ErrCredentialRevoked
	}
	current, err := s.keys.Current(ctx)
	if err != nil {
		return Metadata{}, normalizeKeyError(err)
	}
	if err := current.validate(); err != nil {
		return Metadata{}, ErrKeyUnavailable
	}
	if current.ID() == record.KeyID {
		return Metadata{}, ErrAlreadyCurrentKey
	}
	old, err := s.keys.Lookup(ctx, record.KeyID)
	if err != nil {
		return Metadata{}, normalizeKeyError(err)
	}
	working, err := open(old, record.AccountID, record.Version, record.Nonce, record.Ciphertext)
	if err != nil {
		return Metadata{}, err
	}
	defer clear(working)
	version := record.Version + 1
	nonce, ciphertext, err := seal(current, accountID, version, working)
	if err != nil {
		return Metadata{}, err
	}
	rotated := Record{
		AccountID:  accountID,
		Version:    version,
		KeyID:      current.ID(),
		Nonce:      nonce,
		Ciphertext: ciphertext,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  at,
	}
	audit, err := newAudit(accountID, OperationRotate, actor, version, current.ID(), at)
	if err != nil {
		return Metadata{}, err
	}
	if err := s.backend.ApplyCredentialMutation(Mutation{AccountID: accountID, Record: &rotated, Audit: audit}); err != nil {
		return Metadata{}, err
	}
	return metadataFromRecord(rotated), nil
}

// Rotate is an alias for RotateKey.
func (s *Service) Rotate(ctx context.Context, accountID, actor string, at time.Time) (Metadata, error) {
	return s.RotateKey(ctx, accountID, actor, at)
}

// Revoke marks a credential unusable and wipes its ciphertext. Repeating the
// operation is idempotent and does not create another audit event.
func (s *Service) Revoke(ctx context.Context, accountID, actor string, at time.Time) error {
	if err := s.validateCall(ctx, accountID, actor, at); err != nil {
		return err
	}
	record, found, err := s.backend.GetCredential(accountID)
	if err != nil {
		return err
	}
	if !found {
		return ErrCredentialNotFound
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if at.Before(record.UpdatedAt) {
		return ErrInvalidCredential
	}
	if record.RevokedAt != nil {
		return nil
	}
	if s.invalidator != nil {
		if err := s.invalidator.Invalidate(ctx, accountID, at); err != nil {
			return ErrSessionInvalidation
		}
	}
	revokedAt := at
	revoked := record
	revoked.UpdatedAt = at
	revoked.RevokedAt = &revokedAt
	revoked.Nonce = nil
	revoked.Ciphertext = nil
	audit, err := newAudit(accountID, OperationRevoke, actor, record.Version, record.KeyID, at)
	if err != nil {
		return err
	}
	return s.backend.ApplyCredentialMutation(Mutation{AccountID: accountID, Record: &revoked, Audit: audit})
}

// Metadata returns safe credential metadata and never decrypts the record.
func (s *Service) Metadata(ctx context.Context, accountID string) (Metadata, error) {
	if err := checkContext(ctx); err != nil {
		return Metadata{}, err
	}
	if err := validateIdentifier(accountID, "account id"); err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	record, found, err := s.backend.GetCredential(accountID)
	if err != nil {
		return Metadata{}, err
	}
	if !found {
		return Metadata{}, ErrCredentialNotFound
	}
	if err := record.Validate(); err != nil {
		return Metadata{}, err
	}
	return metadataFromRecord(record), nil
}

// Audits returns credential operation metadata in deterministic order.
func (s *Service) Audits(ctx context.Context, accountID string) ([]Audit, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validateIdentifier(accountID, "account id"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	return s.backend.ListCredentialAudits(accountID)
}

func (s *Service) validateCall(ctx context.Context, accountID, actor string, at time.Time) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateIdentifier(accountID, "account id"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	if err := validateIdentifier(actor, "actor"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	if at.IsZero() {
		return ErrInvalidCredential
	}
	return nil
}

func validateIdentifier(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidService
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func normalizeKeyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrKeyUnavailable
}

func newAudit(accountID, operation, actor string, version uint64, keyID string, at time.Time) (Audit, error) {
	randomID, err := randomID()
	if err != nil {
		return Audit{}, err
	}
	audit := Audit{
		AuditID:    randomID,
		AccountID:  accountID,
		Operation:  operation,
		Actor:      actor,
		Version:    version,
		KeyID:      keyID,
		OccurredAt: at,
	}
	if err := audit.Validate(); err != nil {
		return Audit{}, err
	}
	return audit, nil
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("credential: generate audit id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func associatedData(accountID string, version uint64, keyID string) []byte {
	return []byte("chuzi credential v1\x00" + accountID + "\x00" + strconv.FormatUint(version, 10) + "\x00" + keyID)
}

func seal(key Key, accountID string, version uint64, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key.material)
	if err != nil {
		return nil, nil, ErrKeyUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, ErrKeyUnavailable
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("credential: generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, associatedData(accountID, version, key.ID()))
	return nonce, ciphertext, nil
}

func open(key Key, accountID string, version uint64, nonce, ciphertext []byte) ([]byte, error) {
	if err := key.validate(); err != nil {
		return nil, ErrKeyUnavailable
	}
	block, err := aes.NewCipher(key.material)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, ErrAuthentication
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, associatedData(accountID, version, key.ID()))
	if err != nil {
		return nil, ErrAuthentication
	}
	if len(plaintext) > MaxPayloadSize {
		clear(plaintext)
		return nil, ErrAuthentication
	}
	return plaintext, nil
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
