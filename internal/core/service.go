// Package core coordinates the durable control-plane services behind the
// transport-neutral coreapi contract. It does not own business-state rules;
// internal/account remains the only state-machine authority.
package core

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/observability"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
	maxAuditReadLimit = 10000
)

var (
	ErrInvalidService = errors.New("core: invalid service")
	ErrInvalidQuery   = errors.New("core: invalid query")
)

// RequestPort is the narrow command boundary consumed from request.Service.
type RequestPort interface {
	Submit(requestservice.SubmitInput) (store.Request, bool, error)
	Status(string) (store.Request, error)
	Cancel(string, string, string) (store.Request, error)
}

// StoreReader is intentionally read-only. The concrete bbolt store remains
// behind the Core facade and cannot leak through coreapi DTOs.
type StoreReader interface {
	GetAccount(string) (account.Snapshot, error)
	ListAuditEntries(store.AuditQuery) ([]store.AuditEntry, error)
	ListNotifications() ([]store.Notification, error)
}

type Dependencies struct {
	Requests RequestPort
	Store    StoreReader
}

type Service struct {
	requests RequestPort
	store    StoreReader
}

var _ coreapi.API = (*Service)(nil)

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Requests == nil || dependencies.Store == nil {
		return nil, ErrInvalidService
	}
	return &Service{requests: dependencies.Requests, store: dependencies.Store}, nil
}

func (s *Service) SubmitRequest(ctx context.Context, input coreapi.SubmitRequest) (coreapi.Request, bool, error) {
	if err := s.ready(); err != nil {
		return coreapi.Request{}, false, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.Request{}, false, err
	}
	if err := validateSubmit(input); err != nil {
		return coreapi.Request{}, false, classify(err)
	}
	request, idempotent, err := s.requests.Submit(requestservice.SubmitInput{
		RequestID: input.RequestID, AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		NotificationRoomID: input.NotificationRoomID, Actor: input.Actor, Deadline: input.Deadline,
	})
	if err != nil {
		return coreapi.Request{}, false, classify(err)
	}
	return projectRequest(request), idempotent, nil
}

func (s *Service) GetRequest(ctx context.Context, requestID string) (coreapi.Request, error) {
	if err := s.ready(); err != nil {
		return coreapi.Request{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.Request{}, err
	}
	if strings.TrimSpace(requestID) == "" {
		return coreapi.Request{}, classify(requestservice.ErrInvalidInput)
	}
	request, err := s.requests.Status(requestID)
	if err != nil {
		return coreapi.Request{}, classify(err)
	}
	return projectRequest(request), nil
}

func (s *Service) GetAccount(ctx context.Context, accountID string) (coreapi.Account, error) {
	if err := s.ready(); err != nil {
		return coreapi.Account{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.Account{}, err
	}
	if strings.TrimSpace(accountID) == "" {
		return coreapi.Account{}, classify(store.ErrInvalidAccount)
	}
	snapshot, err := s.store.GetAccount(accountID)
	if err != nil {
		return coreapi.Account{}, classify(err)
	}
	return projectAccount(snapshot), nil
}

func (s *Service) CancelRequest(ctx context.Context, input coreapi.CancelRequest) (coreapi.Request, error) {
	if err := s.ready(); err != nil {
		return coreapi.Request{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.Request{}, err
	}
	if err := validateCancel(input); err != nil {
		return coreapi.Request{}, classify(err)
	}
	request, err := s.requests.Cancel(input.RequestID, input.Actor, input.Reason)
	if err != nil {
		return coreapi.Request{}, classify(err)
	}
	return projectRequest(request), nil
}

func (s *Service) GetResult(ctx context.Context, requestID string) (coreapi.Result, error) {
	request, err := s.GetRequest(ctx, requestID)
	if err != nil {
		return coreapi.Result{}, err
	}
	return coreapi.Result{
		RequestID: request.RequestID,
		State:     request.State,
		Outcome:   outcome(request.State),
		Failure:   request.LastFailure,
		Attempt:   request.Attempt,
		UpdatedAt: request.UpdatedAt,
	}, nil
}

func (s *Service) ListEvents(ctx context.Context, query coreapi.EventQuery) ([]coreapi.Event, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validateQuery(query.AccountID, query.RequestID, query.Since, query.Until, query.Limit); err != nil {
		return nil, classify(err)
	}
	limit := normalizedLimit(query.Limit)
	readLimit := limit
	if query.RequestID != "" {
		readLimit = maxAuditReadLimit
	}
	entries, err := s.store.ListAuditEntries(store.AuditQuery{
		AccountID: query.AccountID, Since: query.Since, Until: query.Until, Limit: readLimit,
	})
	if err != nil {
		return nil, classify(err)
	}
	result := make([]coreapi.Event, 0, min(len(entries), limit))
	requestLabel := observability.RedactIdentifier(query.RequestID)
	for _, entry := range entries {
		if requestLabel != "" && entry.RequestID != requestLabel {
			continue
		}
		result = append(result, projectEvent(entry))
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *Service) ListNotifications(ctx context.Context, query coreapi.NotificationQuery) ([]coreapi.Notification, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validateQuery(query.AccountID, query.RequestID, query.Since, query.Until, query.Limit); err != nil {
		return nil, classify(err)
	}
	limit := normalizedLimit(query.Limit)
	notifications, err := s.store.ListNotifications()
	if err != nil {
		return nil, classify(err)
	}
	result := make([]coreapi.Notification, 0, min(len(notifications), limit))
	for _, notification := range notifications {
		if query.AccountID != "" && notification.AccountID != query.AccountID ||
			query.RequestID != "" && notification.RequestID != query.RequestID ||
			!within(notification.OccurredAt, query.Since, query.Until) {
			continue
		}
		result = append(result, projectNotification(notification))
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return classify(context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return classify(err)
	}
	return nil
}

func (s *Service) ready() error {
	if s == nil || s.requests == nil || s.store == nil {
		return classify(ErrInvalidService)
	}
	return nil
}

func validateSubmit(input coreapi.SubmitRequest) error {
	if !validToken(input.RequestID) || !validToken(input.AccountID) || !validToken(input.IdempotencyKey) {
		return requestservice.ErrInvalidInput
	}
	if input.NotificationRoomID != "" && !validToken(input.NotificationRoomID) {
		return requestservice.ErrInvalidInput
	}
	if input.Actor != "" && !validToken(input.Actor) {
		return requestservice.ErrInvalidInput
	}
	return nil
}

func validateCancel(input coreapi.CancelRequest) error {
	if !validToken(input.RequestID) || (input.Actor != "" && !validToken(input.Actor)) ||
		(input.Reason != "" && !validText(input.Reason, 256)) {
		return requestservice.ErrInvalidInput
	}
	return nil
}

func validateQuery(accountID, requestID string, since, until time.Time, limit int) error {
	if (accountID != "" && !validToken(accountID)) || (requestID != "" && !validToken(requestID)) {
		return ErrInvalidQuery
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return ErrInvalidQuery
	}
	if limit < 0 || limit > maxQueryLimit {
		return ErrInvalidQuery
	}
	return nil
}

func validToken(value string) bool {
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

func validText(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func normalizedLimit(limit int) int {
	if limit == 0 {
		return defaultQueryLimit
	}
	return limit
}

func within(at, since, until time.Time) bool {
	return (since.IsZero() || !at.Before(since)) && (until.IsZero() || !at.After(until))
}

func projectRequest(request store.Request) coreapi.Request {
	return coreapi.Request{
		RequestID: request.RequestID, Account: observability.RedactIdentifier(request.AccountID),
		State: string(request.State), Attempt: request.Attempt, LastFailure: string(request.LastFailure),
		CreatedAt: request.CreatedAt, UpdatedAt: request.UpdatedAt, NotBefore: request.NotBefore, Deadline: request.Deadline,
	}
}

func projectAccount(snapshot account.Snapshot) coreapi.Account {
	return coreapi.Account{
		Account: observability.RedactIdentifier(snapshot.AccountID), State: string(snapshot.Status),
		RequestID: snapshot.RequestID, Revision: snapshot.Revision,
	}
}

func projectEvent(entry store.AuditEntry) coreapi.Event {
	return coreapi.Event{
		Kind: string(entry.Kind), EventID: entry.AuditID, At: entry.At, Account: entry.Account,
		RequestID: entry.RequestID, Operation: entry.Operation, From: string(entry.From),
		To: string(entry.To), Actor: entry.Actor, Version: entry.Version, Resource: entry.Resource,
	}
}

func projectNotification(notification store.Notification) coreapi.Notification {
	status := "pending"
	if !notification.DeliveredAt.IsZero() {
		status = "delivered"
	} else if notification.ClaimedBy != "" {
		status = "claimed"
	}
	return coreapi.Notification{
		EventID: notification.EventID, Account: observability.RedactIdentifier(notification.AccountID),
		RequestID: notification.RequestID, State: string(notification.State), Failure: string(notification.Failure),
		Status: status, Attempt: notification.Attempt, OccurredAt: notification.OccurredAt,
		DeliveredAt: notification.DeliveredAt,
	}
}

func outcome(state string) string {
	switch account.Status(state) {
	case account.LoginSucceeded:
		return "succeeded"
	case account.LoginFailed:
		return "failed"
	case account.Cancelled:
		return "cancelled"
	case account.Blocked:
		return "blocked"
	case account.Expired:
		return "expired"
	case account.Queued, account.Starting, account.LoggingIn:
		return "pending"
	default:
		return "none"
	}
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*coreapi.Error); ok {
		return err
	}
	code := coreapi.CodeInternal
	switch {
	case errors.Is(err, context.Canceled):
		code = coreapi.CodeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		code = coreapi.CodeDeadline
	case errors.Is(err, requestservice.ErrInvalidInput), errors.Is(err, ErrInvalidQuery),
		errors.Is(err, store.ErrInvalidAccount), errors.Is(err, store.ErrInvalidRequest),
		errors.Is(err, store.ErrInvalidNotification), errors.Is(err, account.ErrInvalidEvent),
		errors.Is(err, account.ErrInvalidSnapshot):
		code = coreapi.CodeInvalidArgument
	case errors.Is(err, requestservice.ErrNotAllowed):
		code = coreapi.CodeForbidden
	case errors.Is(err, store.ErrAccountNotFound), errors.Is(err, store.ErrRequestNotFound),
		errors.Is(err, store.ErrLeaseNotFound):
		code = coreapi.CodeNotFound
	case errors.Is(err, store.ErrAccountExists), errors.Is(err, store.ErrRequestExists),
		errors.Is(err, store.ErrRequestConflict), errors.Is(err, store.ErrIdempotencyConflict),
		errors.Is(err, store.ErrRequestStateMismatch), errors.Is(err, store.ErrAccountBusy),
		errors.Is(err, account.ErrEventConflict), errors.Is(err, account.ErrStaleEvent),
		errors.Is(err, account.ErrInvalidTransition):
		code = coreapi.CodeConflict
	case errors.Is(err, store.ErrQueueCapacity):
		code = coreapi.CodeUnavailable
	}
	return coreapi.NewError(code, stableMessage(code))
}

func stableMessage(code coreapi.Code) string {
	switch code {
	case coreapi.CodeInvalidArgument:
		return "request is invalid"
	case coreapi.CodeNotFound:
		return "resource was not found"
	case coreapi.CodeConflict:
		return "request conflicts with current state"
	case coreapi.CodeForbidden:
		return "operation is not allowed"
	case coreapi.CodeUnavailable:
		return "core is temporarily unavailable"
	case coreapi.CodeCancelled:
		return "operation was cancelled"
	case coreapi.CodeDeadline:
		return "operation deadline exceeded"
	default:
		return "core operation failed"
	}
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
