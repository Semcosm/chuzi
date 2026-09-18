package store

import (
	"fmt"
	"os"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

// OperationalSnapshot is a point-in-time, identifier-free view suitable for
// health pages, metrics, and operator diagnostics.
type OperationalSnapshot struct {
	At                        time.Time `json:"at"`
	SchemaVersion             uint64    `json:"schema_version"`
	DatabaseBytes             int64     `json:"database_bytes"`
	Accounts                  int       `json:"accounts"`
	Requests                  int       `json:"requests"`
	QueuedRequests            int       `json:"queued_requests"`
	DelayedRequests           int       `json:"delayed_requests"`
	DeadlineRequests          int       `json:"deadline_requests"`
	RunningRequests           int       `json:"running_requests"`
	ActiveLeases              int       `json:"active_leases"`
	ExpiredLeases             int       `json:"expired_leases"`
	PendingNotifications      int       `json:"pending_notifications"`
	ClaimedNotifications      int       `json:"claimed_notifications"`
	ExpiredNotificationClaims int       `json:"expired_notification_claims"`
	DeliveredNotifications    int       `json:"delivered_notifications"`
}

// OperationalIssue is a stable category/count pair; it deliberately carries
// no account, room, request, or provider identifiers.
type OperationalIssue struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// OperationalSnapshot reads all operational buckets in one bbolt view. The
// returned counts are deterministic for the supplied timestamp.
func (s *Store) OperationalSnapshot(now time.Time) (OperationalSnapshot, error) {
	if s == nil || s.db == nil {
		return OperationalSnapshot{}, bbolt.ErrDatabaseNotOpen
	}
	if now.IsZero() {
		return OperationalSnapshot{}, ErrInvalidQueueOptions
	}
	result := OperationalSnapshot{At: now.UTC()}
	if info, err := os.Stat(s.cfg.DatabasePath()); err == nil {
		result.DatabaseBytes = info.Size()
	}
	version, err := s.SchemaVersion()
	if err != nil {
		return OperationalSnapshot{}, err
	}
	result.SchemaVersion = version
	err = s.view(func(tx *bbolt.Tx) error {
		accounts := tx.Bucket([]byte(migrations.AccountsBucket))
		requests := tx.Bucket([]byte(migrations.RequestsBucket))
		leases := tx.Bucket([]byte(migrations.LeasesBucket))
		notifications := tx.Bucket([]byte(migrations.MatrixNotificationsBucket))
		if accounts == nil || requests == nil || leases == nil || notifications == nil {
			return fmt.Errorf("%w: operational bucket is missing", ErrCorruptData)
		}
		if err := accounts.ForEach(func(key, value []byte) error {
			if value == nil {
				return nil
			}
			result.Accounts++
			if _, err := snapshotFromTx(tx, string(key)); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		if err := requests.ForEach(func(key, value []byte) error {
			if value == nil {
				return nil
			}
			request, err := requestFromTx(tx, string(key))
			if err != nil {
				return err
			}
			result.Requests++
			switch request.State {
			case account.Queued:
				result.QueuedRequests++
				if request.NotBefore.After(now) {
					result.DelayedRequests++
				}
				if !request.Deadline.IsZero() && !now.Before(request.Deadline) {
					result.DeadlineRequests++
				}
			case account.Starting, account.LoggingIn:
				result.RunningRequests++
			}
			return nil
		}); err != nil {
			return err
		}
		if err := leases.ForEach(func(key, value []byte) error {
			if value == nil {
				return nil
			}
			lease, _, err := leaseFromTx(tx, string(key))
			if err != nil {
				return err
			}
			if lease.Expired(now) {
				result.ExpiredLeases++
			} else {
				result.ActiveLeases++
			}
			return nil
		}); err != nil {
			return err
		}
		return notifications.ForEach(func(key, value []byte) error {
			if value == nil {
				return nil
			}
			var notification Notification
			if err := decode(value, &notification); err != nil {
				return err
			}
			if err := notification.Validate(); err != nil {
				return fmt.Errorf("%w: notification %q: %v", ErrCorruptData, string(key), err)
			}
			if !notification.DeliveredAt.IsZero() {
				result.DeliveredNotifications++
				return nil
			}
			result.PendingNotifications++
			if notification.ClaimedBy != "" {
				result.ClaimedNotifications++
				if !now.Before(notification.ClaimExpiresAt) {
					result.ExpiredNotificationClaims++
				}
			}
			return nil
		})
	})
	return result, err
}

func (s *Store) OperationalIssues(now time.Time) ([]OperationalIssue, error) {
	snapshot, err := s.OperationalSnapshot(now)
	if err != nil {
		return nil, err
	}
	return operationalIssues(snapshot), nil
}

func operationalIssues(snapshot OperationalSnapshot) []OperationalIssue {
	issues := make([]OperationalIssue, 0, 5)
	for _, item := range []OperationalIssue{
		{Code: "expired_lease", Count: snapshot.ExpiredLeases},
		{Code: "expired_notification_claim", Count: snapshot.ExpiredNotificationClaims},
		{Code: "deadline_request", Count: snapshot.DeadlineRequests},
	} {
		if item.Count > 0 {
			issues = append(issues, item)
		}
	}
	return issues
}

// Diagnostics is the concise name used by CLI and background health callers.
func (s *Store) Diagnostics(now time.Time) (OperationalSnapshot, []OperationalIssue, error) {
	snapshot, err := s.OperationalSnapshot(now)
	if err != nil {
		return OperationalSnapshot{}, nil, err
	}
	return snapshot, operationalIssues(snapshot), nil
}
