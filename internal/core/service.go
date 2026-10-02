// Package core coordinates the durable control-plane services behind the
// transport-neutral coreapi contract. It does not own business-state rules;
// internal/account remains the only state-machine authority.
package core

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/diagnostics"
	"github.com/Semcosm/chuzi/internal/observability"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/slot"
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
	ListRequests() ([]store.Request, error)
	ListAuditEntries(store.AuditQuery) ([]store.AuditEntry, error)
	QueryNotifications(store.NotificationQuery) ([]store.Notification, error)
}

type BrowserViewPort interface {
	Snapshot(context.Context, string, int, int) (browser.ViewSnapshot, error)
}

type RDPCapabilityPort interface {
	Issue(context.Context, string, string, string) (coreapi.RDPCapability, error)
}

// BoundRDPCapabilityPort is the Windows-slot form. The legacy Issue method is
// retained for non-slot deployments and existing transport adapters.
type BoundRDPCapabilityPort interface {
	IssueBound(context.Context, credential.RDPAuthorization) (coreapi.RDPCapability, error)
}

type accountLeaseReader interface {
	GetLease(string) (account.Lease, bool, error)
}

type slotLeaseReader interface {
	ListSlotLeases() ([]store.SlotLeaseRecord, error)
}

type DiagnosticsPort interface {
	Submit(context.Context, diagnostics.ReportInput) (diagnostics.Status, error)
}

type JobPoolStatusPort interface {
	SlotPoolStatus(string, time.Time) (slot.StatusCounts, error)
}

type Dependencies struct {
	Requests       RequestPort
	Store          StoreReader
	Views          BrowserViewPort
	RDP            RDPCapabilityPort
	Diagnostics    DiagnosticsPort
	JobPools       JobPoolStatusPort
	JobPoolID      string
	MaxConcurrency int
	Clock          func() time.Time
}

type Service struct {
	requests       RequestPort
	store          StoreReader
	views          BrowserViewPort
	rdp            RDPCapabilityPort
	diagnostics    DiagnosticsPort
	jobPools       JobPoolStatusPort
	jobPoolID      string
	maxConcurrency int
	clock          func() time.Time
}

var _ coreapi.API = (*Service)(nil)

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Requests == nil || dependencies.Store == nil {
		return nil, ErrInvalidService
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{requests: dependencies.Requests, store: dependencies.Store, views: dependencies.Views, rdp: dependencies.RDP, diagnostics: dependencies.Diagnostics, jobPools: dependencies.JobPools, jobPoolID: dependencies.JobPoolID, maxConcurrency: dependencies.MaxConcurrency, clock: clock}, nil
}

func (s *Service) GetJobPoolStatus(ctx context.Context, poolID string) (coreapi.JobPoolStatus, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPoolStatus{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPoolStatus{}, err
	}
	if strings.TrimSpace(poolID) == "" {
		poolID = s.jobPoolID
	}
	if !validToken(poolID) || s.jobPools == nil {
		return coreapi.JobPoolStatus{}, coreapi.NewError(coreapi.CodeUnavailable, "job pool status is unavailable")
	}
	now := s.clock()
	if now.IsZero() {
		return coreapi.JobPoolStatus{}, coreapi.NewError(coreapi.CodeInternal, "job pool status is unavailable")
	}
	status, err := s.jobPools.SlotPoolStatus(poolID, now)
	if err != nil {
		return coreapi.JobPoolStatus{}, classify(err)
	}
	effective := status.Ready
	if s.maxConcurrency > 0 && effective > s.maxConcurrency {
		effective = s.maxConcurrency
	}
	return coreapi.JobPoolStatus{PoolID: status.PoolID, EnvironmentID: status.EnvironmentID, EnvironmentVersion: status.EnvironmentVersion, Desired: status.Desired, Ready: status.Ready, Leased: status.Leased, Quarantined: status.Quarantined, Draining: status.Draining, Provisioning: status.Provisioning, Retiring: status.Retiring, Unprovisioned: status.Unprovisioned, EffectiveCapacity: effective}, nil
}

func (s *Service) SubmitDiagnosticReport(ctx context.Context, input coreapi.DiagnosticReport) (coreapi.DiagnosticStatus, error) {
	if err := s.ready(); err != nil {
		return coreapi.DiagnosticStatus{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.DiagnosticStatus{}, err
	}
	if s.diagnostics == nil {
		return coreapi.DiagnosticStatus{}, coreapi.NewError(coreapi.CodeUnavailable, "diagnostics are unavailable")
	}
	status, err := s.diagnostics.Submit(ctx, diagnostics.ReportInput{Severity: input.Severity, Category: input.Category, Summary: input.Summary})
	if err != nil {
		if errors.Is(err, diagnostics.ErrDisabled) {
			return coreapi.DiagnosticStatus{}, coreapi.NewError(coreapi.CodeUnavailable, "diagnostics are unavailable")
		}
		if errors.Is(err, diagnostics.ErrInvalidReport) {
			return coreapi.DiagnosticStatus{}, classify(requestservice.ErrInvalidInput)
		}
		return coreapi.DiagnosticStatus{}, coreapi.NewError(coreapi.CodeInternal, "diagnostic report failed")
	}
	return coreapi.DiagnosticStatus{ID: status.ID, State: status.State, Attempts: status.Attempts, CreatedAt: status.CreatedAt, UpdatedAt: status.UpdatedAt}, nil
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

// IssueRDPCapability authorizes an interactive session without exposing the
// durable account identifier or connection material to the caller.
func (s *Service) IssueRDPCapability(ctx context.Context, input coreapi.RDPCapabilityRequest) (coreapi.RDPCapability, error) {
	if err := s.ready(); err != nil {
		return coreapi.RDPCapability{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.RDPCapability{}, err
	}
	if !validToken(input.RequestID) || !validToken(input.Actor) {
		return coreapi.RDPCapability{}, classify(requestservice.ErrInvalidInput)
	}
	if s.rdp == nil {
		return coreapi.RDPCapability{}, coreapi.NewError(coreapi.CodeUnavailable, "interactive RDP is unavailable")
	}
	request, err := s.requests.Status(input.RequestID)
	if err != nil {
		return coreapi.RDPCapability{}, classify(err)
	}
	switch request.State {
	case account.Starting, account.LoggingIn, account.LoginSucceeded:
		// Interactive access is limited to a request with an active or
		// successfully established session.
	default:
		return coreapi.RDPCapability{}, coreapi.NewError(coreapi.CodeForbidden, "interactive RDP is not authorized for this request")
	}
	var capability coreapi.RDPCapability
	if bound, ok := s.rdp.(BoundRDPCapabilityPort); ok {
		authorization, authErr := s.rdpAuthorization(request, input.Actor)
		if authErr != nil {
			return coreapi.RDPCapability{}, classify(authErr)
		}
		capability, err = bound.IssueBound(ctx, authorization)
	} else {
		capability, err = s.rdp.Issue(ctx, request.AccountID, request.RequestID, input.Actor)
	}
	if err != nil {
		return coreapi.RDPCapability{}, classify(err)
	}
	return capability, nil
}

func (s *Service) rdpAuthorization(request store.Request, actor string) (credential.RDPAuthorization, error) {
	authorization := credential.RDPAuthorization{AccountID: request.AccountID, RequestID: request.RequestID, Actor: actor}
	now := s.clock()
	if now.IsZero() {
		return credential.RDPAuthorization{}, coreapi.NewError(coreapi.CodeUnavailable, "interactive RDP lease is unavailable")
	}
	if reader, ok := s.store.(accountLeaseReader); ok {
		lease, found, err := reader.GetLease(request.AccountID)
		if err != nil {
			return credential.RDPAuthorization{}, err
		}
		if !found || lease.Expired(now) {
			return credential.RDPAuthorization{}, coreapi.NewError(coreapi.CodeForbidden, "interactive RDP lease is unavailable")
		}
		authorization.AccountLeaseID = lease.LeaseID
	}
	if reader, ok := s.store.(slotLeaseReader); ok {
		leases, err := reader.ListSlotLeases()
		if err != nil {
			return credential.RDPAuthorization{}, err
		}
		found := false
		for _, record := range leases {
			if record.Lease.RequestID == request.RequestID && record.Lease.AccountID == request.AccountID && !record.Lease.Expired(now) {
				authorization.SlotLeaseID = record.Lease.LeaseID
				authorization.SlotID = record.SlotID
				authorization.EnvironmentGeneration = record.Lease.EnvironmentGeneration
				found = true
				break
			}
		}
		if !found {
			return credential.RDPAuthorization{}, coreapi.NewError(coreapi.CodeForbidden, "interactive RDP slot lease is unavailable")
		}
	}
	return authorization, nil
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

// ListRequests returns a bounded, deterministic page of safe request
// projections. Store ordering is creation time plus request ID; filtering and
// pagination happen before projection so callers never receive internal
// request records or an unbounded response. The current Store reader scans
// the durable request set to preserve this global ordering.
func (s *Service) ListRequests(ctx context.Context, query coreapi.RequestQuery) ([]coreapi.Request, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validateRequestQuery(query); err != nil {
		return nil, classify(err)
	}
	limit := normalizedLimit(query.Limit)
	requests, err := s.store.ListRequests()
	if err != nil {
		return nil, classify(err)
	}
	// Store.ListRequests already guarantees this ordering. Keep the Core
	// projection deterministic even when a test double or future reader does
	// not preserve that implementation detail.
	sort.SliceStable(requests, func(i, j int) bool {
		if requests[i].CreatedAt.Equal(requests[j].CreatedAt) {
			return requests[i].RequestID < requests[j].RequestID
		}
		return requests[i].CreatedAt.Before(requests[j].CreatedAt)
	})
	result := make([]coreapi.Request, 0, min(limit, len(requests)))
	matched := 0
	for _, request := range requests {
		if query.State != "" && string(request.State) != query.State {
			continue
		}
		if matched < query.Offset {
			matched++
			continue
		}
		result = append(result, projectRequest(request))
		matched++
		if len(result) == limit {
			break
		}
	}
	return result, nil
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

func validateRequestQuery(query coreapi.RequestQuery) error {
	if query.State != "" && !account.Status(query.State).Valid() {
		return ErrInvalidQuery
	}
	if query.Offset < 0 || query.Offset > maxQueryOffset || query.Limit < 0 || query.Limit > maxQueryLimit {
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
	case errors.Is(err, requestservice.ErrRateLimited):
		code = coreapi.CodeRateLimited
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
	case errors.Is(err, slot.ErrSlotUnavailable), errors.Is(err, slot.ErrPoolNotFound):
		code = coreapi.CodeUnavailable
	case errors.Is(err, browser.ErrViewUnavailable):
		code = coreapi.CodeUnavailable
	case errors.Is(err, credential.ErrRDPUnauthorized), errors.Is(err, credential.ErrRDPRevoked):
		code = coreapi.CodeForbidden
	case errors.Is(err, credential.ErrRDPExpired), errors.Is(err, credential.ErrRDPCapability):
		code = coreapi.CodeConflict
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
	case coreapi.CodeRateLimited:
		return "request rate limit exceeded"
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
