package credential

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var credentialTestTime = time.Date(2026, time.September, 10, 13, 0, 0, 0, time.UTC)

type memoryBackend struct {
	mu     sync.Mutex
	record Record
	found  bool
	audits []Audit
}

type fakeInvalidator struct {
	calls int
	err   error
}

func (f *fakeInvalidator) Invalidate(context.Context, string, time.Time) error {
	f.calls++
	return f.err
}

func (m *memoryBackend) GetCredential(accountID string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.found || m.record.AccountID != accountID {
		return Record{}, false, nil
	}
	return cloneRecord(m.record), true, nil
}

func (m *memoryBackend) ApplyCredentialMutation(mutation Mutation) error {
	if err := mutation.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.audits {
		if existing.AuditID == mutation.Audit.AuditID {
			if existing.Operation == mutation.Audit.Operation && existing.Actor == mutation.Audit.Actor && existing.OccurredAt.Equal(mutation.Audit.OccurredAt) {
				return nil
			}
			return ErrAuditConflict
		}
	}
	if mutation.Record != nil {
		m.record = cloneRecord(*mutation.Record)
		m.found = true
	}
	m.audits = append(m.audits, mutation.Audit)
	return nil
}

func (m *memoryBackend) ListCredentialAudits(accountID string) ([]Audit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Audit, 0, len(m.audits))
	for _, audit := range m.audits {
		if audit.AccountID == accountID {
			result = append(result, audit)
		}
	}
	return result, nil
}

func cloneRecord(record Record) Record {
	result := record
	result.Nonce = append([]byte(nil), record.Nonce...)
	result.Ciphertext = append([]byte(nil), record.Ciphertext...)
	if record.RevokedAt != nil {
		revokedAt := *record.RevokedAt
		result.RevokedAt = &revokedAt
	}
	return result
}

func testService(t *testing.T) (*Service, *memoryBackend, *StaticKeyring) {
	t.Helper()
	first, err := NewKey("key-1", bytesOf(0x11, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := NewStaticKeyring(first)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{}
	service, err := New(backend, keyring)
	if err != nil {
		t.Fatal(err)
	}
	return service, backend, keyring
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestPutUseDoesNotExposeDurablePlaintext(t *testing.T) {
	service, backend, _ := testService(t)
	plaintext := []byte("authorized-user\x00password")
	metadata, err := service.Put(context.Background(), "account-1", plaintext, "operator", credentialTestTime)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Version != 1 || metadata.KeyID != "key-1" {
		t.Fatalf("metadata = %#v", metadata)
	}
	stored, found, err := backend.GetCredential("account-1")
	if err != nil || !found {
		t.Fatalf("stored credential = %#v/%t: %v", stored, found, err)
	}
	encoded, _ := json.Marshal(stored)
	if strings.Contains(string(encoded), string(plaintext)) {
		t.Fatal("credential plaintext was persisted")
	}
	var seen []byte
	if err := service.Use(context.Background(), "account-1", "runner", credentialTestTime.Add(time.Minute), func(value []byte) error {
		seen = value
		if string(value) != string(plaintext) {
			t.Fatalf("decrypted value = %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for index, value := range seen {
		if value != 0 {
			t.Fatalf("plaintext callback buffer byte %d was not cleared", index)
		}
	}
	audits, err := service.Audits(context.Background(), "account-1")
	if err != nil || len(audits) != 2 || audits[0].Operation != OperationStore || audits[1].Operation != OperationAccess {
		t.Fatalf("audits = %#v, %v", audits, err)
	}
}

func TestRotateUsesHistoricalKeyAndRevokeWipesCiphertext(t *testing.T) {
	service, backend, keyring := testService(t)
	if _, err := service.Put(context.Background(), "account-1", []byte("secret"), "operator", credentialTestTime); err != nil {
		t.Fatal(err)
	}
	second, err := NewKey("key-2", bytesOf(0x22, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := keyring.SetCurrent(second); err != nil {
		t.Fatal(err)
	}
	metadata, err := service.RotateKey(context.Background(), "account-1", "operator", credentialTestTime.Add(time.Minute))
	if err != nil || metadata.Version != 2 || metadata.KeyID != "key-2" {
		t.Fatalf("rotated metadata = %#v, %v", metadata, err)
	}
	var got string
	if err := service.Use(context.Background(), "account-1", "runner", credentialTestTime.Add(2*time.Minute), func(value []byte) error {
		got = string(value)
		return nil
	}); err != nil || got != "secret" {
		t.Fatalf("rotated use = %q, %v", got, err)
	}
	if err := service.Revoke(context.Background(), "account-1", "operator", credentialTestTime.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(context.Background(), "account-1", "operator", credentialTestTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.GetCredential("account-1"); err != nil || !found {
		t.Fatal(err)
	} else if len(backend.record.Nonce) != 0 || len(backend.record.Ciphertext) != 0 || backend.record.RevokedAt == nil {
		t.Fatalf("revoked record retained material: %#v", backend.record)
	}
	if err := service.Use(context.Background(), "account-1", "runner", credentialTestTime.Add(5*time.Minute), func([]byte) error { return nil }); !errors.Is(err, ErrCredentialRevoked) {
		t.Fatalf("revoked use error = %v", err)
	}
	audits, err := service.Audits(context.Background(), "account-1")
	if err != nil || len(audits) != 4 {
		t.Fatalf("rotation/revoke audits = %#v, %v", audits, err)
	}
}

func TestTamperAndMissingKeyFailWithoutPlaintext(t *testing.T) {
	service, backend, keyring := testService(t)
	if _, err := service.Put(context.Background(), "account-1", []byte("secret"), "operator", credentialTestTime); err != nil {
		t.Fatal(err)
	}
	backend.record.Ciphertext[0] ^= 0x80
	if err := service.Use(context.Background(), "account-1", "runner", credentialTestTime, func([]byte) error { return nil }); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampered credential error = %v", err)
	}
	keyring.keys = map[string]Key{}
	if err := service.Use(context.Background(), "account-1", "runner", credentialTestTime, func([]byte) error { return nil }); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestRevokeFailsClosedWhenSessionsCannotBeInvalidated(t *testing.T) {
	_, backend, keyring := testService(t)
	invalidator := &fakeInvalidator{err: errors.New("session still running")}
	service, err := NewWithSessionInvalidator(backend, keyring, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Put(context.Background(), "account-1", []byte("secret"), "operator", credentialTestTime); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(context.Background(), "account-1", "operator", credentialTestTime.Add(time.Minute)); !errors.Is(err, ErrSessionInvalidation) {
		t.Fatalf("failed invalidation error = %v", err)
	}
	metadata, err := service.Metadata(context.Background(), "account-1")
	if err != nil || metadata.RevokedAt != nil {
		t.Fatalf("credential after failed invalidation = %#v, %v", metadata, err)
	}
	if len(backend.audits) != 1 || invalidator.calls != 1 {
		t.Fatalf("failed invalidation side effects = audits:%d calls:%d", len(backend.audits), invalidator.calls)
	}
	invalidator.err = nil
	if err := service.Revoke(context.Background(), "account-1", "operator", credentialTestTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if invalidator.calls != 2 || len(backend.audits) != 2 {
		t.Fatalf("successful invalidation side effects = audits:%d calls:%d", len(backend.audits), invalidator.calls)
	}
}

func TestEnvKeyringUsesDeploymentVariablesOnly(t *testing.T) {
	t.Setenv("TEST_CREDENTIAL_KEY_ID", "env-key")
	t.Setenv("TEST_CREDENTIAL_KEY", "ERERERERERERERERERERERERERERERERERERERERERE=")
	keyring := NewEnvKeyring("TEST_CREDENTIAL_KEY", "TEST_CREDENTIAL_KEY_ID")
	key, err := keyring.Current(context.Background())
	if err != nil || key.ID() != "env-key" {
		t.Fatalf("env key = %q, %v", key.ID(), err)
	}
	t.Setenv("TEST_CREDENTIAL_KEY", "not-a-key")
	if _, err := keyring.Current(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("invalid env key error = %v", err)
	}
	_ = os.Getenv("TEST_CREDENTIAL_KEY")
}

func TestEnvSourceAndInjectEncryptsAndWipesInput(t *testing.T) {
	service, backend, _ := testService(t)
	t.Setenv("CHUZI_TEST_CREDENTIAL", "injected-secret")
	source, err := NewEnvSource("CHUZI_TEST_CREDENTIAL")
	if err != nil {
		t.Fatal(err)
	}
	at := credentialTestTime.Add(time.Hour)
	if _, err := service.Inject(context.Background(), "account-1", "operator", at, source); err != nil {
		t.Fatal(err)
	}
	record, found, err := backend.GetCredential("account-1")
	if err != nil || !found || string(record.Ciphertext) == "injected-secret" {
		t.Fatalf("injected record = %#v, found=%t, err=%v", record, found, err)
	}
	if err := service.Use(context.Background(), "account-1", "runner", at.Add(time.Minute), func(value []byte) error {
		if string(value) != "injected-secret" {
			t.Fatalf("decrypted injected value = %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
