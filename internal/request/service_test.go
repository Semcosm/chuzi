package request

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/store"
)

func newRateLimitService(t *testing.T, now *time.Time, limits RateLimitConfig) (*Service, *store.Store) {
	t.Helper()
	cfg, err := config.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	counter := 0
	service, err := NewWithConfig(database, func() time.Time { return *now }, func(kind string) string {
		counter++
		return fmt.Sprintf("%s-%d", kind, counter)
	}, "service", Config{RateLimit: limits, Sink: observability.NopSink{}})
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}

func createRateLimitAccounts(t *testing.T, database *store.Store, count int) {
	t.Helper()
	for index := 1; index <= count; index++ {
		if _, err := database.CreateAccount(fmt.Sprintf("account-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
}

func submitRateLimitRequest(t *testing.T, service *Service, index int, actor, room string) (store.Request, bool, error) {
	t.Helper()
	return service.Submit(SubmitInput{
		RequestID: fmt.Sprintf("request-%d", index), AccountID: fmt.Sprintf("account-%d", index),
		IdempotencyKey: fmt.Sprintf("idempotency-%d", index), Actor: actor, NotificationRoomID: room,
	})
}

func TestRateLimitGlobalWindowIsDeterministic(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	service, database := newRateLimitService(t, &now, RateLimitConfig{GlobalLimit: 2, GlobalWindow: time.Minute})
	createRateLimitAccounts(t, database, 3)
	for index := 1; index <= 2; index++ {
		if _, idempotent, err := submitRateLimitRequest(t, service, index, fmt.Sprintf("user-%d", index), fmt.Sprintf("room-%d", index)); err != nil || idempotent {
			t.Fatalf("submit %d = idempotent=%v, err=%v", index, idempotent, err)
		}
	}
	if _, _, err := submitRateLimitRequest(t, service, 3, "user-3", "room-3"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third submission error = %v, want ErrRateLimited", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := submitRateLimitRequest(t, service, 3, "user-3", "room-3"); err != nil {
		t.Fatalf("submission after exact window = %v", err)
	}
}

func TestRateLimitActorAndRoomDimensionsAreIndependent(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	service, database := newRateLimitService(t, &now, RateLimitConfig{
		ActorLimit: 1, ActorWindow: time.Minute, RoomLimit: 1, RoomWindow: time.Minute,
	})
	createRateLimitAccounts(t, database, 4)
	if _, _, err := submitRateLimitRequest(t, service, 1, "alice", "room-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := submitRateLimitRequest(t, service, 2, "alice", "room-b"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("same actor error = %v, want ErrRateLimited", err)
	}
	if _, _, err := submitRateLimitRequest(t, service, 2, "bob", "room-a"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("same room error = %v, want ErrRateLimited", err)
	}
	if _, _, err := submitRateLimitRequest(t, service, 2, "bob", "room-b"); err != nil {
		t.Fatalf("different actor and room error = %v", err)
	}
}

func TestRateLimitIdempotentRetryDoesNotConsumeQuotaAndQueriesRemainAvailable(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	service, database := newRateLimitService(t, &now, RateLimitConfig{GlobalLimit: 1, GlobalWindow: time.Minute})
	createRateLimitAccounts(t, database, 2)
	input := SubmitInput{RequestID: "request-1", AccountID: "account-1", IdempotencyKey: "idempotency-1", Actor: "alice", NotificationRoomID: "room-a"}
	first, idempotent, err := service.Submit(input)
	if err != nil || idempotent || first.RequestID != input.RequestID {
		t.Fatalf("first submit = %#v, idempotent=%v, err=%v", first, idempotent, err)
	}
	second, idempotent, err := service.Submit(input)
	if err != nil || !idempotent || second.RequestID != first.RequestID {
		t.Fatalf("idempotent retry = %#v, idempotent=%v, err=%v", second, idempotent, err)
	}
	if _, err := service.Status(first.RequestID); err != nil {
		t.Fatalf("status while rate limited = %v", err)
	}
	if _, err := service.Cancel(first.RequestID, "alice", "test cancel"); err != nil {
		t.Fatalf("cancel while rate limited = %v", err)
	}
	if _, _, err := submitRateLimitRequest(t, service, 2, "bob", "room-b"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("new request after idempotent retry = %v, want ErrRateLimited", err)
	}
}

func TestRateLimitReservationRollsBackWhenDurableSubmissionFails(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	service, database := newRateLimitService(t, &now, RateLimitConfig{GlobalLimit: 1, GlobalWindow: time.Minute})
	input := SubmitInput{RequestID: "request-1", AccountID: "account-1", IdempotencyKey: "idempotency-1", Actor: "alice"}
	if _, _, err := service.Submit(input); !errors.Is(err, store.ErrAccountNotFound) {
		t.Fatalf("failed submission error = %v, want ErrAccountNotFound", err)
	}
	if _, err := database.CreateAccount(input.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Submit(input); err != nil {
		t.Fatalf("submission after rollback = %v", err)
	}
}

func TestRateLimitIdempotencyConflictDoesNotConsumeNewRequestQuota(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	service, database := newRateLimitService(t, &now, RateLimitConfig{GlobalLimit: 1, GlobalWindow: time.Minute})
	createRateLimitAccounts(t, database, 2)
	first := SubmitInput{RequestID: "request-1", AccountID: "account-1", IdempotencyKey: "shared-key", Actor: "alice"}
	if _, _, err := service.Submit(first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.RequestID = "request-2"
	conflict.AccountID = "account-2"
	if _, _, err := service.Submit(conflict); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := submitRateLimitRequest(t, service, 2, "bob", "room-b"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("new request after conflict = %v, want ErrRateLimited", err)
	}
}

func TestRateLimitConfigRequiresWindowForEnabledDimension(t *testing.T) {
	if _, err := newRateLimiter(RateLimitConfig{GlobalLimit: 1}); err == nil {
		t.Fatal("enabled limiter without a window was accepted")
	}
	if _, err := newRateLimiter(RateLimitConfig{GlobalLimit: -1}); err == nil {
		t.Fatal("negative limiter was accepted")
	}
}
