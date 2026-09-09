package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

// GetCredential returns the encrypted record for an account. The store never
// decrypts it; callers should normally use internal/credential.Service rather
// than this low-level backend method.
func (s *Store) GetCredential(accountID string) (credential.Record, bool, error) {
	if strings.TrimSpace(accountID) == "" {
		return credential.Record{}, false, credential.ErrInvalidCredential
	}
	var result credential.Record
	var found bool
	err := s.view(func(tx *bbolt.Tx) error {
		records := tx.Bucket([]byte(migrations.CredentialsBucket))
		raw := records.Get([]byte(accountID))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("%w: decode credential record: %v", ErrCorruptData, err)
		}
		if err := result.Validate(); err != nil {
			return fmt.Errorf("%w: invalid credential record: %v", ErrCorruptData, err)
		}
		found = true
		return nil
	})
	return result, found, err
}

// ApplyCredentialMutation atomically writes an encrypted record, when
// supplied, and its audit record. Repeating the same audit ID and content is
// idempotent; conflicting reuse is rejected.
func (s *Store) ApplyCredentialMutation(mutation credential.Mutation) error {
	if err := mutation.Validate(); err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		if tx.Bucket([]byte(migrations.AccountsBucket)).Get([]byte(mutation.AccountID)) == nil {
			return ErrAccountNotFound
		}
		records := tx.Bucket([]byte(migrations.CredentialsBucket))
		auditRoot := tx.Bucket([]byte(migrations.CredentialAuditsBucket))
		audits, err := auditRoot.CreateBucketIfNotExists([]byte(mutation.AccountID))
		if err != nil {
			return fmt.Errorf("create credential audit bucket: %w", err)
		}
		if raw := audits.Get([]byte(mutation.Audit.AuditID)); raw != nil {
			var existing credential.Audit
			if err := json.Unmarshal(raw, &existing); err != nil {
				return fmt.Errorf("%w: decode credential audit: %v", ErrCorruptData, err)
			}
			if !credentialAuditsEqual(existing, mutation.Audit) {
				return credential.ErrAuditConflict
			}
			if mutation.Record != nil {
				rawRecord := records.Get([]byte(mutation.AccountID))
				if rawRecord == nil {
					return credential.ErrAuditConflict
				}
				var existingRecord credential.Record
				if err := json.Unmarshal(rawRecord, &existingRecord); err != nil {
					return fmt.Errorf("%w: decode credential record: %v", ErrCorruptData, err)
				}
				if !credentialRecordsEqual(existingRecord, *mutation.Record) {
					return credential.ErrAuditConflict
				}
			}
			return nil
		}

		if mutation.Record != nil {
			raw := records.Get([]byte(mutation.AccountID))
			if raw != nil {
				var existing credential.Record
				if err := json.Unmarshal(raw, &existing); err != nil {
					return fmt.Errorf("%w: decode credential record: %v", ErrCorruptData, err)
				}
				if err := existing.Validate(); err != nil {
					return fmt.Errorf("%w: invalid credential record: %v", ErrCorruptData, err)
				}
				if existing.Version > mutation.Record.Version ||
					(existing.Version == mutation.Record.Version && mutation.Audit.Operation != credential.OperationRevoke) {
					return credential.ErrVersionConflict
				}
				if existing.Version == mutation.Record.Version && existing.RevokedAt != nil {
					return credential.ErrVersionConflict
				}
			}
			encoded, err := json.Marshal(mutation.Record)
			if err != nil {
				return fmt.Errorf("encode credential record: %w", err)
			}
			if err := records.Put([]byte(mutation.AccountID), encoded); err != nil {
				return fmt.Errorf("write credential record: %w", err)
			}
		}
		encodedAudit, err := json.Marshal(mutation.Audit)
		if err != nil {
			return fmt.Errorf("encode credential audit: %w", err)
		}
		if err := audits.Put([]byte(mutation.Audit.AuditID), encodedAudit); err != nil {
			return fmt.Errorf("write credential audit: %w", err)
		}
		return nil
	})
}

// ListCredentialAudits returns metadata-only credential audits in deterministic
// occurrence order.
func (s *Store) ListCredentialAudits(accountID string) ([]credential.Audit, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, credential.ErrInvalidCredential
	}
	result := make([]credential.Audit, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(migrations.CredentialAuditsBucket))
		audits := root.Bucket([]byte(accountID))
		if audits == nil {
			return nil
		}
		return audits.ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var audit credential.Audit
			if err := json.Unmarshal(raw, &audit); err != nil {
				return fmt.Errorf("%w: decode credential audit: %v", ErrCorruptData, err)
			}
			if err := audit.Validate(); err != nil || audit.AccountID != accountID {
				return fmt.Errorf("%w: invalid credential audit", ErrCorruptData)
			}
			result = append(result, audit)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].AuditID < result[j].AuditID
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result, nil
}

func credentialAuditsEqual(left, right credential.Audit) bool {
	return left.AuditID == right.AuditID && left.AccountID == right.AccountID &&
		left.Operation == right.Operation && left.Actor == right.Actor &&
		left.Version == right.Version && left.KeyID == right.KeyID &&
		left.OccurredAt.Equal(right.OccurredAt)
}

func credentialRecordsEqual(left, right credential.Record) bool {
	if left.AccountID != right.AccountID || left.Version != right.Version || left.KeyID != right.KeyID ||
		!left.CreatedAt.Equal(right.CreatedAt) || !left.UpdatedAt.Equal(right.UpdatedAt) {
		return false
	}
	if !bytes.Equal(left.Nonce, right.Nonce) || !bytes.Equal(left.Ciphertext, right.Ciphertext) {
		return false
	}
	if left.RevokedAt == nil || right.RevokedAt == nil {
		return left.RevokedAt == nil && right.RevokedAt == nil
	}
	return left.RevokedAt.Equal(*right.RevokedAt)
}
