package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

type AuditKind string

const (
	StateAudit      AuditKind = "state"
	CredentialAudit AuditKind = "credential"
)

// AuditQuery bounds a read-only global audit query. AccountID is an internal
// filter; returned entries always contain a stable redacted account label.
type AuditQuery struct {
	AccountID string
	RequestID string
	Since     time.Time
	Until     time.Time
	Limit     int
}

// AuditEntry is the intentionally narrow, metadata-only shape exposed to
// operators. It omits state-event reason, raw actor/account IDs, key material,
// ciphertext, cookies, and page/provider content.
type AuditEntry struct {
	Kind      AuditKind      `json:"kind"`
	AuditID   string         `json:"audit_id"`
	At        time.Time      `json:"at"`
	Account   string         `json:"account"`
	RequestID string         `json:"request_id,omitempty"`
	Operation string         `json:"operation"`
	From      account.Status `json:"from,omitempty"`
	To        account.Status `json:"to,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Version   uint64         `json:"version,omitempty"`
	Resource  string         `json:"resource,omitempty"`
}

func (e AuditEntry) Validate() error {
	if e.Kind != StateAudit && e.Kind != CredentialAudit ||
		strings.TrimSpace(e.AuditID) == "" || !strings.HasPrefix(e.AuditID, "id_") ||
		e.At.IsZero() || strings.TrimSpace(e.Account) == "" || !strings.HasPrefix(e.Account, "id_") ||
		strings.TrimSpace(e.Operation) == "" {
		return ErrCorruptData
	}
	if e.Actor != "" && !strings.HasPrefix(e.Actor, "id_") {
		return ErrCorruptData
	}
	if e.Resource != "" && !strings.HasPrefix(e.Resource, "id_") {
		return ErrCorruptData
	}
	if e.Kind == StateAudit {
		if !e.From.Valid() || !e.To.Valid() || e.RequestID == "" {
			return ErrCorruptData
		}
	} else if e.Version == 0 {
		return ErrCorruptData
	}
	return nil
}

// ListAuditEntries returns state and credential audit metadata in one stable,
// read-only view. The query is bounded so an operator endpoint cannot
// accidentally allocate memory proportional to an unbounded database.
func (s *Store) ListAuditEntries(query AuditQuery) ([]AuditEntry, error) {
	if s == nil || s.db == nil {
		return nil, bbolt.ErrDatabaseNotOpen
	}
	if strings.TrimSpace(query.AccountID) != query.AccountID || strings.TrimSpace(query.RequestID) != query.RequestID {
		return nil, ErrInvalidAccount
	}
	if !query.Since.IsZero() && !query.Until.IsZero() && query.Until.Before(query.Since) {
		return nil, fmt.Errorf("%w: audit interval is inverted", ErrInvalidRequest)
	}
	limit := query.Limit
	if limit == 0 {
		limit = 1000
	}
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("%w: audit limit is out of range", ErrInvalidRequest)
	}
	result := make([]AuditEntry, 0)
	err := s.view(func(tx *bbolt.Tx) error {
		accounts := tx.Bucket([]byte(migrations.AccountsBucket))
		auditRoot := tx.Bucket([]byte(migrations.AuditsBucket))
		credentialAuditRoot := tx.Bucket([]byte(migrations.CredentialAuditsBucket))
		if accounts == nil || auditRoot == nil || credentialAuditRoot == nil {
			return fmt.Errorf("%w: audit bucket is missing", ErrCorruptData)
		}
		accountIDs := make([]string, 0)
		if query.AccountID != "" {
			if accounts.Get([]byte(query.AccountID)) == nil {
				return ErrAccountNotFound
			}
			accountIDs = append(accountIDs, query.AccountID)
		} else if err := accounts.ForEach(func(key, value []byte) error {
			if value != nil {
				accountIDs = append(accountIDs, string(key))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, accountID := range accountIDs {
			if _, err := snapshotFromTx(tx, accountID); err != nil {
				return err
			}
			audits := auditRoot.Bucket([]byte(accountID))
			if audits != nil {
				if err := audits.ForEach(func(_, raw []byte) error {
					if raw == nil {
						return nil
					}
					var audit account.AuditRecord
					if err := decode(raw, &audit); err != nil {
						return err
					}
					if query.RequestID != "" && audit.Event.RequestID != query.RequestID {
						return nil
					}
					before := len(result)
					if err := appendStateAudit(&result, audit); err != nil {
						return err
					}
					if !withinAuditWindow(result[len(result)-1].At, query) {
						result = result[:before]
					} else {
						trimAuditEntries(&result, limit)
					}
					return nil
				}); err != nil {
					return err
				}
			}
			credentialAudits := credentialAuditRoot.Bucket([]byte(accountID))
			if credentialAudits != nil {
				if err := credentialAudits.ForEach(func(_, raw []byte) error {
					if raw == nil {
						return nil
					}
					var audit credential.Audit
					if err := decode(raw, &audit); err != nil {
						return err
					}
					if err := audit.Validate(); err != nil || audit.AccountID != accountID {
						return fmt.Errorf("%w: invalid credential audit", ErrCorruptData)
					}
					entry := AuditEntry{
						Kind:      CredentialAudit,
						AuditID:   observability.RedactIdentifier(audit.AuditID),
						At:        audit.OccurredAt,
						Account:   observability.RedactIdentifier(audit.AccountID),
						Operation: audit.Operation,
						Actor:     observability.RedactIdentifier(audit.Actor),
						Version:   audit.Version,
						Resource:  observability.RedactIdentifier(audit.KeyID),
					}
					if query.RequestID != "" {
						return nil
					}
					if withinAuditWindow(entry.At, query) {
						if err := entry.Validate(); err != nil {
							return fmt.Errorf("%w: invalid credential audit entry", ErrCorruptData)
						}
						appendBoundedAuditEntry(&result, entry, limit)
					}
					return nil
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool { return auditEntryLess(result[i], result[j]) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// ListAudits is a compatibility-friendly shorthand for ListAuditEntries.
func (s *Store) ListAudits(query AuditQuery) ([]AuditEntry, error) {
	return s.ListAuditEntries(query)
}

func appendStateAudit(result *[]AuditEntry, audit account.AuditRecord) error {
	entry := AuditEntry{
		Kind:      StateAudit,
		AuditID:   observability.RedactIdentifier(audit.Event.EventID),
		At:        audit.Event.OccurredAt,
		Account:   observability.RedactIdentifier(audit.Event.AccountID),
		RequestID: observability.RedactIdentifier(audit.Event.RequestID),
		Operation: "transition",
		From:      audit.Event.From,
		To:        audit.Event.To,
		Actor:     observability.RedactIdentifier(audit.Event.Actor),
	}
	if err := entry.Validate(); err != nil {
		return fmt.Errorf("%w: invalid state audit entry", ErrCorruptData)
	}
	*result = append(*result, entry)
	return nil
}

// appendBoundedAuditEntry keeps the query result bounded while the bbolt
// cursor is still walking every account. The final result is the earliest
// entries under the deterministic ordering used by the operator view.
func appendBoundedAuditEntry(result *[]AuditEntry, entry AuditEntry, limit int) {
	*result = append(*result, entry)
	trimAuditEntries(result, limit)
}

func trimAuditEntries(result *[]AuditEntry, limit int) {
	if len(*result) <= limit {
		return
	}
	sort.SliceStable(*result, func(i, j int) bool { return auditEntryLess((*result)[i], (*result)[j]) })
	*result = (*result)[:limit]
}

func auditEntryLess(left, right AuditEntry) bool {
	if !left.At.Equal(right.At) {
		return left.At.Before(right.At)
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.AuditID < right.AuditID
}

func withinAuditWindow(at time.Time, query AuditQuery) bool {
	if !query.Since.IsZero() && at.Before(query.Since) {
		return false
	}
	if !query.Until.IsZero() && at.After(query.Until) {
		return false
	}
	return true
}
