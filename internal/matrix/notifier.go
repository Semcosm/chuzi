package matrix

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/store"
)

var (
	ErrInvalidNotifier = errors.New("matrix: invalid notifier")
	ErrSendFailed      = errors.New("matrix: notification send failed")
)

// Sender is implemented by the deployment-specific Matrix client. EventID is
// a stable transaction identifier and must be used for downstream deduping.
type Sender interface {
	Send(context.Context, string, string, string) error
}

// NotifierConfig controls one outbox delivery worker.
type NotifierConfig struct {
	Owner        string
	ClaimTTL     time.Duration
	RetryBase    time.Duration
	RetryMax     time.Duration
	BatchSize    int
	Clock        func() time.Time
	Sink         observability.Sink
}

// Notifier drains the durable Matrix outbox without owning business state.
type Notifier struct {
	store  *store.Store
	sender Sender
	config NotifierConfig
}

// DeliveryResult reports one deterministic delivery pass.
type DeliveryResult struct {
	Claimed   int
	Delivered int
	Retried   int
}

// NewNotifier validates and constructs an outbox delivery worker.
func NewNotifier(database *store.Store, sender Sender, config NotifierConfig) (*Notifier, error) {
	if database == nil || sender == nil || config.Owner == "" || config.ClaimTTL <= 0 ||
		config.RetryBase < 0 || config.RetryMax < config.RetryBase || config.BatchSize < 1 {
		return nil, ErrInvalidNotifier
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Sink == nil {
		config.Sink = observability.NopSink{}
	}
	return &Notifier{store: database, sender: sender, config: config}, nil
}

// Flush claims and delivers at most BatchSize notifications. A sender failure
// is classified and scheduled for retry without exposing the underlying error.
func (n *Notifier) Flush(ctx context.Context) (DeliveryResult, error) {
	if n == nil || ctx == nil {
		return DeliveryResult{}, ErrInvalidNotifier
	}
	if err := ctx.Err(); err != nil {
		return DeliveryResult{}, err
	}
	now := n.config.Clock()
	if now.IsZero() {
		return DeliveryResult{}, ErrInvalidNotifier
	}
	claimed, err := n.store.ClaimNotifications(now, n.config.Owner, n.config.ClaimTTL, n.config.BatchSize)
	if err != nil {
		return DeliveryResult{}, err
	}
	result := DeliveryResult{Claimed: len(claimed)}
	for _, notification := range claimed {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		body, err := RenderNotification(notification)
		if err != nil {
			return result, err
		}
		if err := n.sender.Send(ctx, notification.RoomID, notification.EventID, body); err != nil {
			next := now.Add(n.retryDelay(notification.Attempt))
			if retryErr := n.store.RetryNotification(notification.EventID, n.config.Owner, now, next); retryErr != nil {
				return result, retryErr
			}
			result.Retried++
			n.config.Sink.Record(observability.Event{
				At:         now,
				Component:  "matrix",
				Operation:  "notify",
				Outcome:    "retry",
				RequestID:  notification.RequestID,
				Resource:   observability.RedactIdentifier(notification.RoomID),
				ErrorClass: "send_failed",
			})
			continue
		}
		if err := n.store.CompleteNotification(notification.EventID, n.config.Owner, now); err != nil {
			return result, err
		}
		result.Delivered++
		n.config.Sink.Record(observability.Event{
			At:         now,
			Component:  "matrix",
			Operation:  "notify",
			Outcome:    "delivered",
			RequestID:  notification.RequestID,
			Resource:   observability.RedactIdentifier(notification.RoomID),
		})
	}
	return result, nil
}

func (n *Notifier) retryDelay(attempt int) time.Duration {
	if n.config.RetryBase == 0 {
		return 0
	}
	delay := n.config.RetryBase
	for step := 1; step < attempt && delay < n.config.RetryMax; step++ {
		if delay > n.config.RetryMax/2 {
			return n.config.RetryMax
		}
		delay *= 2
	}
	if delay > n.config.RetryMax {
		return n.config.RetryMax
	}
	return delay
}

// RenderNotification converts a safe projection into the public Matrix text.
// It deliberately omits room IDs, actors, reasons, stack traces and secrets.
func RenderNotification(notification store.Notification) (string, error) {
	if err := notification.Validate(); err != nil {
		return "", fmt.Errorf("%w: invalid notification", ErrSendFailed)
	}
	body := fmt.Sprintf("[chuzi] account=%s request=%s status=%s time=%s",
		observability.RedactIdentifier(notification.AccountID),
		notification.RequestID,
		notification.State,
		notification.OccurredAt.UTC().Format(time.RFC3339),
	)
	if notification.Failure != "" {
		body += " failure=" + string(notification.Failure)
	}
	return body, nil
}
