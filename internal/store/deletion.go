package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrDeletionNotFound   = errors.New("store: deletion not found")
	ErrDeletionConflict   = errors.New("store: deletion conflict")
	ErrDeletionInProgress = errors.New("store: deletion already in progress")
	ErrAccountDeleted     = errors.New("store: account is deleted")
)

type DeletionRequest struct {
	DeletionID    string
	AccountID     string
	Actor         string
	ReasonClass   string
	ProfilePolicy account.ProfilePolicy
	RequestedAt   time.Time
}

func (s *Store) RequestDeletion(input DeletionRequest) (account.DeletionSnapshot, bool, error) {
	if strings.TrimSpace(input.DeletionID) != input.DeletionID || strings.TrimSpace(input.AccountID) != input.AccountID || strings.TrimSpace(input.Actor) != input.Actor || strings.TrimSpace(input.ReasonClass) != input.ReasonClass || input.RequestedAt.IsZero() {
		return account.DeletionSnapshot{}, false, fmt.Errorf("%w: invalid deletion request", account.ErrInvalidDeletion)
	}
	if input.ProfilePolicy == "" {
		input.ProfilePolicy = account.ProfilePurge
	}
	var result account.DeletionSnapshot
	var idempotent bool
	err := s.update(func(tx *bbolt.Tx) error {
		accounts := tx.Bucket([]byte(migrations.AccountsBucket))
		if accounts.Get([]byte(input.AccountID)) == nil {
			return ErrAccountNotFound
		}
		bucket := tx.Bucket([]byte(migrations.AccountDeletionsBucket))
		if raw := bucket.Get([]byte(input.AccountID)); raw != nil {
			var existing account.DeletionSnapshot
			if err := decode(raw, &existing); err != nil {
				return fmt.Errorf("%w: decode deletion: %v", ErrCorruptData, err)
			}
			if err := existing.Validate(); err != nil {
				return fmt.Errorf("%w: invalid deletion: %v", ErrCorruptData, err)
			}
			if existing.DeletionID == input.DeletionID {
				if existing.Actor == input.Actor && existing.ReasonClass == input.ReasonClass && existing.ProfilePolicy == input.ProfilePolicy {
					result, idempotent = existing, true
					return nil
				}
				return ErrDeletionConflict
			}
			if existing.Stage == account.DeletionTombstoned {
				return ErrAccountDeleted
			}
			return ErrDeletionInProgress
		}
		state, err := snapshotFromTx(tx, input.AccountID)
		if err != nil {
			return err
		}
		deletion, err := account.NewDeletion(input.DeletionID, input.AccountID, observability.RedactIdentifier(input.AccountID), input.Actor, input.ReasonClass, input.ProfilePolicy, state.Status, input.RequestedAt)
		if err != nil {
			return err
		}
		if err := putDeletion(bucket, deletion); err != nil {
			return err
		}
		result = deletion
		return nil
	})
	return result, idempotent, err
}

func (s *Store) GetDeletion(accountID string) (account.DeletionSnapshot, error) {
	deletion, found, err := s.LookupDeletion(accountID)
	if err != nil {
		return account.DeletionSnapshot{}, err
	}
	if !found {
		return account.DeletionSnapshot{}, ErrDeletionNotFound
	}
	return deletion, nil
}

func (s *Store) LookupDeletion(accountID string) (account.DeletionSnapshot, bool, error) {
	if strings.TrimSpace(accountID) != accountID || accountID == "" {
		return account.DeletionSnapshot{}, false, account.ErrInvalidDeletion
	}
	var result account.DeletionSnapshot
	var found bool
	err := s.view(func(tx *bbolt.Tx) error {
		raw := tx.Bucket([]byte(migrations.AccountDeletionsBucket)).Get([]byte(accountID))
		if raw == nil {
			return nil
		}
		if err := decode(raw, &result); err != nil {
			return fmt.Errorf("%w: decode deletion: %v", ErrCorruptData, err)
		}
		if err := result.Validate(); err != nil {
			return fmt.Errorf("%w: invalid deletion: %v", ErrCorruptData, err)
		}
		found = true
		return nil
	})
	return result, found, err
}

func (s *Store) GetDeletionByID(deletionID string) (account.DeletionSnapshot, error) {
	if strings.TrimSpace(deletionID) != deletionID || deletionID == "" {
		return account.DeletionSnapshot{}, account.ErrInvalidDeletion
	}
	var result account.DeletionSnapshot
	err := s.view(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(migrations.AccountDeletionsBucket)).ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var candidate account.DeletionSnapshot
			if err := decode(raw, &candidate); err != nil {
				return fmt.Errorf("%w: decode deletion: %v", ErrCorruptData, err)
			}
			if err := candidate.Validate(); err != nil {
				return fmt.Errorf("%w: invalid deletion: %v", ErrCorruptData, err)
			}
			if candidate.DeletionID == deletionID {
				result = candidate
				return errDeletionFound
			}
			return nil
		})
	})
	if errors.Is(err, errDeletionFound) {
		return result, nil
	}
	if err != nil {
		return account.DeletionSnapshot{}, err
	}
	return account.DeletionSnapshot{}, ErrDeletionNotFound
}

var errDeletionFound = errors.New("store: deletion found")

func (s *Store) AdvanceDeletion(event account.DeletionEvent) (account.DeletionTransitionResult, error) {
	if err := event.Validate(); err != nil {
		return account.DeletionTransitionResult{}, err
	}
	var result account.DeletionTransitionResult
	err := s.update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.AccountDeletionsBucket))
		raw := bucket.Get([]byte(event.AccountID))
		if raw == nil {
			return ErrDeletionNotFound
		}
		var state account.DeletionSnapshot
		if err := decode(raw, &state); err != nil {
			return fmt.Errorf("%w: decode deletion: %v", ErrCorruptData, err)
		}
		var err error
		result, err = account.ApplyDeletion(state, event)
		if err != nil {
			return err
		}
		if result.Idempotent {
			return nil
		}
		return putDeletion(bucket, result.State)
	})
	if err != nil {
		return account.DeletionTransitionResult{}, err
	}
	return result, nil
}

func putDeletion(bucket *bbolt.Bucket, deletion account.DeletionSnapshot) error {
	if bucket == nil {
		return fmt.Errorf("%w: deletion bucket is missing", ErrCorruptData)
	}
	if err := deletion.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(deletion)
	if err != nil {
		return fmt.Errorf("encode deletion: %w", err)
	}
	return bucket.Put([]byte(deletion.AccountID), raw)
}
