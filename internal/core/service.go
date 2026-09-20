// Package core coordinates the durable control-plane services behind the
// transport-neutral coreapi contract. It does not own business-state rules;
// internal/account remains the only state-machine authority.
package core

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/observability"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
	maxQueryOffset    = 100000
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
	QueryNotifications(store.NotificationQuery) ([]store.Notification, error)
}

type BrowserViewPort interface {
	Snapshot(context.Context, string, int, int) (browser.ViewSnapshot, error)
}

type Dependencies struct {
	Requests RequestPort
	Store    StoreReader
	Views    BrowserViewPort
}

type Service struct {
	requests RequestPort
	store    StoreReader
	views    BrowserViewPort
}

var _ coreapi.API = (*Service)(nil)

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Requests == nil || dependencies.Store == nil {
		return nil, ErrInvalidService
	}
	return &Service{requests: dependencies.Requests, store: dependencies.Store, views: dependencies.Views}, nil
}

func (s *Service) GetBrowserView(ctx context.Context, input coreapi.BrowserViewRequest) (coreapi.BrowserView, error) {
	if err := s.ready(); err != nil {
		return coreapi.BrowserView{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.BrowserView{}, err
	}
	if !validToken(input.RequestID) {
		return coreapi.BrowserView{}, classify(requestservice.ErrInvalidInput)
	}
	if s.views == nil {
		return coreapi.BrowserView{}, classify(browser.ErrViewUnavailable)
	}
	width, height := input.Width, input.Height
	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 360
	}
	if width < 160 || width > 1280 || height < 90 || height > 720 {
		return coreapi.BrowserView{}, classify(requestservice.ErrInvalidInput)
	}
	frame, err := s.views.Snapshot(ctx, input.RequestID, width, height)
	if err != nil {
		// A view is an ephemeral observation. Adapter/CDP failures should not
		// expose an internal error classification; only caller cancellation
		// and deadlines retain their transport semantics.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return coreapi.BrowserView{}, classify(err)
		}
		return coreapi.BrowserView{}, classify(browser.ErrViewUnavailable)
	}
	if frame.ContentType != "image/jpeg" || frame.Width < 160 || frame.Width > 1280 ||
		frame.Height < 90 || frame.Height > 720 || len(frame.Data) == 0 || len(frame.Data) > 700<<10 {
		return coreapi.BrowserView{}, classify(browser.ErrViewUnavailable)
	}
	return coreapi.BrowserView{
		RequestID: input.RequestID, ContentType: frame.ContentType,
		Width: frame.Width, Height: frame.Height,
		Data: base64.StdEncoding.EncodeToString(frame.Data), CapturedAt: time.Now().UTC(),
	}, nil
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
	if err := validateQuery(query.AccountID, query.RequestID, query.Since, query.Until, 0, query.Limit); err != nil {
		return nil, classify(err)
	}
	limit := normalizedLimit(query.Limit)
	entries, err := s.store.ListAuditEntries(store.AuditQuery{
		AccountID: query.AccountID, RequestID: query.RequestID, Since: query.Since, Until: query.Until, Limit: limit,
	})
	if err != nil {
		return nil, classify(err)
	}
	result := make([]coreapi.Event, 0, min(len(entries), limit))
	for _, entry := range entries {
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
	if err := validateQuery(query.AccountID, query.RequestID, query.Since, query.Until, query.Offset, query.Limit); err != nil {
		return nil, classify(err)
	}
	limit := normalizedLimit(query.Limit)
	notifications, err := s.store.QueryNotifications(store.NotificationQuery{
		AccountID: query.AccountID,
		RequestID: query.RequestID,
		Since:     query.Since,
		Until:     query.Until,
		Offset:    query.Offset,
		Limit:     limit,
	})
	if err != nil {
		return nil, classify(err)
	}
	result := make([]coreapi.Notification, 0, min(len(notifications), limit))
	for _, notification := range notifications {
		result = append(result, projectNotification(notification))
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

func validateQuery(accountID, requestID string, since, until time.Time, offset, limit int) error {
	if (accountID != "" && !validToken(accountID)) || (requestID != "" && !validToken(requestID)) {
		return ErrInvalidQuery
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return ErrInvalidQuery
	}
	if offset < 0 || offset > maxQueryOffset || limit < 0 || limit > maxQueryLimit {
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
	case errors.Is(err, browser.ErrViewUnavailable):
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
