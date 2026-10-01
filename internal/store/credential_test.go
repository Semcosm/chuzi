package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/credential"
)

func TestStoreCredentialBackendPersistsCiphertextAndAuditsAtomically(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	key, err := credential.NewKey("key-1", bytesForStore(0x31, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := credential.NewStaticKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := credential.New(service, keyring)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.September, 10, 14, 0, 0, 0, time.UTC)
	if _, err := credentials.Put(context.Background(), "account-1", []byte("secret-value"), "operator", at); err != nil {
		t.Fatal(err)
	}
	record, found, err := service.GetCredential("account-1")
	if err != nil || !found || len(record.Ciphertext) == 0 || len(record.Nonce) == 0 {
		t.Fatalf("stored record = %#v/%t: %v", record, found, err)
	}
	if err := credentials.Use(context.Background(), "account-1", "runner", at.Add(time.Minute), func(value []byte) error {
		if string(value) != "secret-value" {
			t.Fatalf("decrypted value = %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	audits, err := credentials.Audits(context.Background(), "account-1")
	if err != nil || len(audits) != 2 {
		t.Fatalf("audits = %#v, %v", audits, err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(service.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedCredentials, err := credential.New(reopened, keyring)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := reopenedCredentials.Use(context.Background(), "account-1", "restart", at.Add(2*time.Minute), func(value []byte) error {
		got = string(value)
		return nil
	}); err != nil || got != "secret-value" {
		t.Fatalf("reopened credential = %q, %v", got, err)
	}
}

func TestStoreCredentialMutationIsIdempotentAndRejectsAuditConflict(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.September, 10, 14, 0, 0, 0, time.UTC)
	key, err := credential.NewKey("key-1", bytesForStore(0x41, 32))
	if err != nil {
		t.Fatal(err)
	}
	record := credential.Record{
		AccountID:  "account-1",
		Version:    1,
		KeyID:      key.ID(),
		Nonce:      bytesForStore(0x01, 12),
		Ciphertext: bytesForStore(0x02, 16),
		CreatedAt:  at,
		UpdatedAt:  at,
	}
	audit := credential.Audit{AuditID: "audit-1", AccountID: "account-1", Operation: credential.OperationStore, Actor: "operator", Version: 1, KeyID: key.ID(), OccurredAt: at}
	mutation := credential.Mutation{AccountID: "account-1", Record: &record, Audit: audit}
	if err := service.ApplyCredentialMutation(mutation); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyCredentialMutation(mutation); err != nil {
		t.Fatalf("idempotent mutation = %v", err)
	}
	conflict := mutation
	conflict.Audit.Actor = "other"
	if err := service.ApplyCredentialMutation(conflict); !errors.Is(err, credential.ErrAuditConflict) {
		t.Fatalf("audit conflict = %v", err)
	}
	next := record
	next.Version = 1
	next.Ciphertext = bytesForStore(0x03, 16)
	nextAudit := audit
	nextAudit.AuditID = "audit-2"
	if err := service.ApplyCredentialMutation(credential.Mutation{AccountID: "account-1", Record: &next, Audit: nextAudit}); !errors.Is(err, credential.ErrVersionConflict) {
		t.Fatalf("version conflict = %v", err)
	}
	revokedAt := at.Add(time.Minute)
	revoked := record
	revoked.Version = 2
	revoked.UpdatedAt = revokedAt
	revoked.RevokedAt = &revokedAt
	revoked.Nonce = nil
	revoked.Ciphertext = nil
	deletionAudit := credential.Audit{AuditID: "audit-delete", AccountID: "account-1", DeletionID: "deletion-1", Operation: credential.OperationRevokeForDeletion, Actor: "operator", Version: 2, KeyID: key.ID(), OccurredAt: revokedAt}
	deletionMutation := credential.Mutation{AccountID: "account-1", Record: &revoked, Audit: deletionAudit}
	if err := service.ApplyCredentialMutation(deletionMutation); err != nil {
		t.Fatal(err)
	}
	conflictingDeletion := deletionMutation
	conflictingDeletion.Audit.DeletionID = "deletion-2"
	if err := service.ApplyCredentialMutation(conflictingDeletion); !errors.Is(err, credential.ErrAuditConflict) {
		t.Fatalf("deletion audit conflict = %v", err)
	}
}

func TestStoreCredentialDeletionRevokeWipesMaterial(t *testing.T) {
	service, _ := openTestStore(t)
	if _, err := service.CreateAccount("account-1"); err != nil {
		t.Fatal(err)
	}
	key, err := credential.NewKey("key-1", bytesForStore(0x51, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := credential.NewStaticKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := credential.New(service, keyring)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.September, 10, 15, 0, 0, 0, time.UTC)
	if _, err := credentials.Put(context.Background(), "account-1", []byte("secret-value"), "operator", at); err != nil {
		t.Fatal(err)
	}
	if err := credentials.RevokeForDeletion(context.Background(), "account-1", "deletion-1", "operator", true, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	record, found, err := service.GetCredential("account-1")
	if err != nil || !found || record.RevokedAt == nil || len(record.Nonce) != 0 || len(record.Ciphertext) != 0 {
		t.Fatalf("deleted credential = %#v/%t: %v", record, found, err)
	}
	if err := credentials.RevokeForDeletion(context.Background(), "account-1", "deletion-1", "operator", true, at.Add(2*time.Minute)); err != nil {
		t.Fatalf("idempotent deletion revoke = %v", err)
	}
	audits, err := credentials.Audits(context.Background(), "account-1")
	if err != nil || len(audits) != 2 || audits[1].Operation != credential.OperationRevokeForDeletion || audits[1].DeletionID != "deletion-1" {
		t.Fatalf("deletion audits = %#v, %v", audits, err)
	}
}

func bytesForStore(value byte, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}
