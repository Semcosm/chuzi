package request

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrRateLimited is returned when a new request exceeds one of the configured
// service submission windows. It is deliberately stable so adapters can map it
// to a public error code without exposing limiter internals.
var ErrRateLimited = errors.New("request: rate limited")

const (
	RateLimitScopeGlobal  = "global"
	RateLimitScopeActor   = "actor"
	RateLimitScopeRoom    = "room"
	RateLimitScopeAccount = "account"
)

// RateLimitConfig describes sliding-window limits for request submission. A
// zero limit disables that dimension. Windows are expressed as durations so
// callers and deterministic tests need no process-global clock.
type RateLimitConfig struct {
	GlobalLimit   int
	GlobalWindow  time.Duration
	ActorLimit    int
	ActorWindow   time.Duration
	RoomLimit     int
	RoomWindow    time.Duration
	AccountLimit  int
	AccountWindow time.Duration
}

func (c RateLimitConfig) Validate() error {
	if err := validateRateLimit(c.GlobalLimit, c.GlobalWindow, RateLimitScopeGlobal); err != nil {
		return err
	}
	if err := validateRateLimit(c.ActorLimit, c.ActorWindow, RateLimitScopeActor); err != nil {
		return err
	}
	if err := validateRateLimit(c.RoomLimit, c.RoomWindow, RateLimitScopeRoom); err != nil {
		return err
	}
	return validateRateLimit(c.AccountLimit, c.AccountWindow, RateLimitScopeAccount)
}

func validateRateLimit(limit int, window time.Duration, scope string) error {
	if limit < 0 || limit > 1_000_000 {
		return fmt.Errorf("request: %s rate limit is out of range", scope)
	}
	if window < 0 || window > 24*time.Hour || (limit > 0 && window <= 0) {
		return fmt.Errorf("request: %s rate limit window is invalid", scope)
	}
	return nil
}

type rateLimitEntry struct {
	at time.Time
	id uint64
}

type rateLimiter struct {
	mu      sync.Mutex
	config  RateLimitConfig
	entries map[string]map[string][]rateLimitEntry
	nextID  uint64
}

func newRateLimiter(config RateLimitConfig) (*rateLimiter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &rateLimiter{config: config, entries: make(map[string]map[string][]rateLimitEntry)}, nil
}

func (l *rateLimiter) enabled() bool {
	return l != nil && (l.config.GlobalLimit > 0 || l.config.ActorLimit > 0 || l.config.RoomLimit > 0 || l.config.AccountLimit > 0)
}

type rateLimitReservation struct {
	limiter *rateLimiter
	id      uint64
	keys    []rateLimitKey
}

type rateLimitKey struct {
	scope string
	key   string
}

// reserve atomically records one candidate submission in every configured
// bucket. The caller rolls it back for idempotent or failed submissions.
func (l *rateLimiter) reserve(input SubmitInput, now time.Time) (*rateLimitReservation, error) {
	if !l.enabled() {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	keys := make([]rateLimitKey, 0, 4)
	if l.config.GlobalLimit > 0 {
		keys = append(keys, rateLimitKey{scope: RateLimitScopeGlobal})
	}
	if l.config.ActorLimit > 0 {
		keys = append(keys, rateLimitKey{scope: RateLimitScopeActor, key: input.Actor})
	}
	if l.config.RoomLimit > 0 && input.NotificationRoomID != "" {
		keys = append(keys, rateLimitKey{scope: RateLimitScopeRoom, key: input.NotificationRoomID})
	}
	if l.config.AccountLimit > 0 {
		keys = append(keys, rateLimitKey{scope: RateLimitScopeAccount, key: input.AccountID})
	}

	for _, key := range keys {
		limit, window := l.limitFor(key.scope)
		entries := l.pruneLocked(key, now, window)
		if len(entries) >= limit {
			return nil, ErrRateLimited
		}
	}
	if l.nextID == ^uint64(0) {
		l.nextID = 1
	} else {
		l.nextID++
	}
	id := l.nextID
	for _, key := range keys {
		l.appendLocked(key, rateLimitEntry{at: now, id: id})
	}
	return &rateLimitReservation{limiter: l, id: id, keys: keys}, nil
}

func (l *rateLimiter) limitFor(scope string) (int, time.Duration) {
	switch scope {
	case RateLimitScopeGlobal:
		return l.config.GlobalLimit, l.config.GlobalWindow
	case RateLimitScopeActor:
		return l.config.ActorLimit, l.config.ActorWindow
	case RateLimitScopeRoom:
		return l.config.RoomLimit, l.config.RoomWindow
	case RateLimitScopeAccount:
		return l.config.AccountLimit, l.config.AccountWindow
	default:
		return 0, 0
	}
}

func (l *rateLimiter) pruneLocked(key rateLimitKey, now time.Time, window time.Duration) []rateLimitEntry {
	byKey := l.entries[key.scope]
	if byKey == nil {
		return nil
	}
	entries := byKey[key.key]
	cutoff := now.Add(-window)
	kept := entries[:0]
	for _, entry := range entries {
		if entry.at.After(cutoff) {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		delete(byKey, key.key)
		return nil
	}
	byKey[key.key] = kept
	return kept
}

func (l *rateLimiter) appendLocked(key rateLimitKey, entry rateLimitEntry) {
	byKey := l.entries[key.scope]
	if byKey == nil {
		byKey = make(map[string][]rateLimitEntry)
		l.entries[key.scope] = byKey
	}
	byKey[key.key] = append(byKey[key.key], entry)
}

func (r *rateLimitReservation) rollback() {
	if r == nil || r.limiter == nil {
		return
	}
	l := r.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range r.keys {
		byKey := l.entries[key.scope]
		entries := byKey[key.key]
		for index := len(entries) - 1; index >= 0; index-- {
			if entries[index].id != r.id {
				continue
			}
			entries = append(entries[:index], entries[index+1:]...)
			break
		}
		if len(entries) == 0 {
			delete(byKey, key.key)
		} else {
			byKey[key.key] = entries
		}
	}
}
