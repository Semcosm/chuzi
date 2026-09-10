package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/migrations"
	"go.etcd.io/bbolt"
)

var (
	ErrNotificationNotFound = errors.New("store: notification not found")
	ErrNotificationLease    = errors.New("store: notification lease is not owned")
	ErrNotificationExpired  = errors.New("store: notification lease is expired")
	ErrInvalidNotification  = errors.New("store: invalid notification")
)

// Notification is the durable, redaction-safe projection consumed by a Matrix
// notifier. It stores identifiers and classified state only; the rendered
// message is produced at delivery time and never persisted.
type Notification struct {
	EventID       string                 `json:"event_id"`
	RoomID        string                 `json:"room_id"`
	AccountID     string                 `json:"account_id"`
	RequestID     string                 `json:"request_id"`
	State         account.Status         `json:"state"`
	Failure       account.FailureClass   `json:"failure,omitempty"`
	OccurredAt    time.Time              `json:"occurred_at"`
	CreatedAt     time.Time              `json:"created_at"`
	NextAttemptAt time.Time              `json:"next_attempt_at"`
	Attempt       int                    `json:"attempt"`
	ClaimedBy     string                 `json:"claimed_by,omitempty"`
	ClaimExpiresAt time.Time              `json:"claim_expires_at,omitempty"`
	DeliveredAt   time.Time              `json:"delivered_at,omitempty"`
}

// Validate checks the durable notification projection and keeps values safe
// for rendering into an external message.
func (n Notification) Validate() error {
	if !safeNotificationToken(n.EventID) || !safeNotificationRoom(n.RoomID) ||
		!safeNotificationToken(n.AccountID) || !safeNotificationToken(n.RequestID) ||
		!n.State.Valid() || n.OccurredAt.IsZero() || n.CreatedAt.IsZero() ||
		n.CreatedAt.Before(n.OccurredAt) || n.NextAttemptAt.IsZero() ||
		n.NextAttemptAt.Before(n.CreatedAt) || n.Attempt < 0 {
		return ErrInvalidNotification
	}
	if n.Failure != "" {
		switch n.Failure {
		case account.TransientFailure, account.CredentialFailure,
			account.PermissionFailure, account.ConfigurationFailure,
			account.UnknownFailure:
		default:
			return ErrInvalidNotification
		}
		if n.State != account.LoginFailed {
			return ErrInvalidNotification
		}
	}
	if n.ClaimedBy == "" && !n.ClaimExpiresAt.IsZero() {
		return ErrInvalidNotification
	}
	if n.ClaimedBy != "" && (!safeNotificationToken(n.ClaimedBy) || n.ClaimExpiresAt.IsZero()) {
		return ErrInvalidNotification
	}
	if !n.DeliveredAt.IsZero() {
		if n.DeliveredAt.Before(n.CreatedAt) || n.ClaimedBy != "" || !n.ClaimExpiresAt.IsZero() {
			return ErrInvalidNotification
		}
	}
	return nil
}

// ListNotifications returns pending and delivered notifications in durable
// creation order. It is intended for diagnostics and restart verification.
func (s *Store) ListNotifications() ([]Notification, error) {
	var result []Notification
	err := s.view(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.MatrixNotificationsBucket))
		if bucket == nil {
			return fmt.Errorf("%w: notification bucket is missing", ErrCorruptData)
		}
		return bucket.ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var notification Notification
			if err := decode(raw, &notification); err != nil {
				return err
			}
			if err := notification.Validate(); err != nil {
				return fmt.Errorf("%w: notification %q: %v", ErrCorruptData, notification.EventID, err)
			}
			result = append(result, notification)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sortNotifications(result)
	return result, nil
}

// ClaimNotifications claims ready notifications for one delivery worker. A
// claim expires so a process restart or network failure can be recovered by a
// later worker without deleting the outbox record.
func (s *Store) ClaimNotifications(now time.Time, owner string, ttl time.Duration, limit int) ([]Notification, error) {
	if now.IsZero() || !safeNotificationToken(owner) || ttl <= 0 || limit < 1 {
		return nil, ErrInvalidNotification
	}
	var result []Notification
	err := s.update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.MatrixNotificationsBucket))
		if bucket == nil {
			return fmt.Errorf("%w: notification bucket is missing", ErrCorruptData)
		}
		candidates := make([]Notification, 0)
		if err := bucket.ForEach(func(_, raw []byte) error {
			if raw == nil {
				return nil
			}
			var notification Notification
			if err := decode(raw, &notification); err != nil {
				return err
			}
			if err := notification.Validate(); err != nil {
				return fmt.Errorf("%w: notification %q: %v", ErrCorruptData, notification.EventID, err)
			}
			if !notification.DeliveredAt.IsZero() || notification.NextAttemptAt.After(now) {
				return nil
			}
			if notification.ClaimedBy != "" && now.Before(notification.ClaimExpiresAt) {
				return nil
			}
			candidates = append(candidates, notification)
			return nil
		}); err != nil {
			return err
		}
		sortNotifications(candidates)
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		for _, notification := range candidates {
			if notification.Attempt == int(^uint(0)>>1) {
				return ErrRequestAttemptOverflow
			}
			notification.Attempt++
			notification.ClaimedBy = owner
			notification.ClaimExpiresAt = now.Add(ttl)
			if err := putNotification(bucket, notification); err != nil {
				return err
			}
			result = append(result, notification)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// CompleteNotification marks one claimed notification as delivered. The
// event ID remains the stable Matrix transaction ID for downstream deduping.
func (s *Store) CompleteNotification(eventID, owner string, deliveredAt time.Time) error {
	if !safeNotificationToken(eventID) || !safeNotificationToken(owner) || deliveredAt.IsZero() {
		return ErrInvalidNotification
	}
	return s.updateNotification(eventID, owner, deliveredAt, func(notification *Notification) error {
		notification.DeliveredAt = deliveredAt
		notification.ClaimedBy = ""
		notification.ClaimExpiresAt = time.Time{}
		return nil
	})
}

// RetryNotification releases one claim and schedules a later delivery. The
// existing event ID is retained so a sender can make retries idempotent.
func (s *Store) RetryNotification(eventID, owner string, now, nextAttemptAt time.Time) error {
	if !safeNotificationToken(eventID) || !safeNotificationToken(owner) || now.IsZero() || nextAttemptAt.IsZero() || nextAttemptAt.Before(now) {
		return ErrInvalidNotification
	}
	return s.updateNotification(eventID, owner, now, func(notification *Notification) error {
		notification.NextAttemptAt = nextAttemptAt
		notification.ClaimedBy = ""
		notification.ClaimExpiresAt = time.Time{}
		return nil
	})
}

func (s *Store) updateNotification(eventID, owner string, now time.Time, mutate func(*Notification) error) error {
	return s.update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(migrations.MatrixNotificationsBucket))
		if bucket == nil {
			return fmt.Errorf("%w: notification bucket is missing", ErrCorruptData)
		}
		raw := bucket.Get([]byte(eventID))
		if raw == nil {
			return ErrNotificationNotFound
		}
		var notification Notification
		if err := decode(raw, &notification); err != nil {
			return err
		}
		if err := notification.Validate(); err != nil {
			return fmt.Errorf("%w: notification %q: %v", ErrCorruptData, eventID, err)
		}
		if !notification.DeliveredAt.IsZero() {
			return nil
		}
		if notification.ClaimedBy != owner {
			return ErrNotificationLease
		}
		if !now.Before(notification.ClaimExpiresAt) {
			return ErrNotificationExpired
		}
		if err := mutate(&notification); err != nil {
			return err
		}
		if err := notification.Validate(); err != nil {
			return fmt.Errorf("%w: updated notification: %v", ErrCorruptData, err)
		}
		return putNotification(bucket, notification)
	})
}

func enqueueNotificationTx(tx *bbolt.Tx, event account.Event, request Request) error {
	if request.NotificationRoomID == "" {
		return nil
	}
	bucket := tx.Bucket([]byte(migrations.MatrixNotificationsBucket))
	if bucket == nil {
		return fmt.Errorf("%w: notification bucket is missing", ErrCorruptData)
	}
	if raw := bucket.Get([]byte(event.EventID)); raw != nil {
		var existing Notification
		if err := decode(raw, &existing); err != nil {
			return err
		}
		if err := existing.Validate(); err != nil {
			return fmt.Errorf("%w: notification %q: %v", ErrCorruptData, event.EventID, err)
		}
		if existing.RoomID != request.NotificationRoomID || existing.RequestID != request.RequestID || existing.State != event.To {
			return fmt.Errorf("%w: notification event %q conflicts with request projection", ErrCorruptData, event.EventID)
		}
		return nil
	}
	notification := Notification{
		EventID:       event.EventID,
		RoomID:        request.NotificationRoomID,
		AccountID:     request.AccountID,
		RequestID:     request.RequestID,
		State:         event.To,
		OccurredAt:    event.OccurredAt,
		CreatedAt:     event.OccurredAt,
		NextAttemptAt: event.OccurredAt,
	}
	if err := notification.Validate(); err != nil {
		return err
	}
	return putNotification(bucket, notification)
}

func setNotificationFailureTx(tx *bbolt.Tx, eventID string, class account.FailureClass) error {
	bucket := tx.Bucket([]byte(migrations.MatrixNotificationsBucket))
	if bucket == nil {
		return fmt.Errorf("%w: notification bucket is missing", ErrCorruptData)
	}
	raw := bucket.Get([]byte(eventID))
	if raw == nil {
		return nil
	}
	var notification Notification
	if err := decode(raw, &notification); err != nil {
		return err
	}
	notification.Failure = class
	if err := notification.Validate(); err != nil {
		return fmt.Errorf("%w: failure classification: %v", ErrCorruptData, err)
	}
	return putNotification(bucket, notification)
}

func putNotification(bucket *bbolt.Bucket, notification Notification) error {
	raw, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	if err := bucket.Put([]byte(notification.EventID), raw); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}
	return nil
}

func sortNotifications(notifications []Notification) {
	sort.Slice(notifications, func(i, j int) bool {
		if notifications[i].CreatedAt.Equal(notifications[j].CreatedAt) {
			return notifications[i].EventID < notifications[j].EventID
		}
		return notifications[i].CreatedAt.Before(notifications[j].CreatedAt)
	})
}

func safeNotificationRoom(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 512 {
		return false
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func safeNotificationToken(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 512 {
		return false
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}
