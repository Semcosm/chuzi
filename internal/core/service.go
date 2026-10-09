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
	"github.com/Semcosm/chuzi/internal/environment"
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

type DiagnosticSnapshotPort interface {
	Snapshot(context.Context, diagnostics.ReportInput) (diagnostics.Report, error)
}

type JobPoolStatusPort interface {
	SlotPoolStatus(string, time.Time) (slot.StatusCounts, error)
}

type JobPoolControlPort interface {
	ListJobPoolProjections(time.Time) ([]store.JobPoolProjection, error)
	GetJobPoolProjection(string, time.Time) (store.JobPoolProjection, error)
	ApplyJobPool(store.JobPoolMutation) (store.JobPoolOperation, bool, error)
	ScaleJobPool(string, int, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error)
	DrainJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error)
	ResumeJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error)
	DeleteJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error)
	GetJobPoolOperation(string) (store.JobPoolOperation, error)
	ReconcileJobPoolControl(string, time.Time) (store.JobPoolProjection, error)
}

type SlotSessionControlPort interface {
	StartSlotSession(store.SlotSessionMutation) (store.SlotSessionOperation, bool, error)
	GetSlotSessionOperation(string) (store.SlotSessionOperation, error)
}

type EnvironmentControlPort interface {
	ListEnvironmentRecords() ([]environment.Record, error)
	ApplyEnvironmentOperation(store.EnvironmentMutation) (store.EnvironmentOperationRecord, bool, error)
	GetEnvironmentOperation(string) (store.EnvironmentOperationRecord, error)
	UpdateEnvironmentOperation(string, string, string, time.Time) (store.EnvironmentOperationRecord, error)
	ApplyEnvironmentGate(string, string, string, time.Time) (environment.Record, error)
}

type EnvironmentOperationFinalizer interface {
	CompleteEnvironmentOperation(string, string, string, environment.Record, time.Time) (store.EnvironmentOperationRecord, error)
}

// EnvironmentExecutor is the controlled package lifecycle boundary. Core
// receives only an opaque catalog reference and never resolves filesystem
// paths itself.
type EnvironmentExecutor interface {
	Execute(context.Context, store.EnvironmentMutation) (environment.Record, error)
}

type Dependencies struct {
	Requests            RequestPort
	Store               StoreReader
	Views               BrowserViewPort
	RDP                 RDPCapabilityPort
	Diagnostics         DiagnosticsPort
	JobPools            JobPoolStatusPort
	JobPoolControl      JobPoolControlPort
	SlotSessions        SlotSessionControlPort
	Environments        EnvironmentControlPort
	EnvironmentExecutor EnvironmentExecutor
	JobPoolID           string
	MaxConcurrency      int
	Clock               func() time.Time
}

type Service struct {
	requests            RequestPort
	store               StoreReader
	views               BrowserViewPort
	rdp                 RDPCapabilityPort
	diagnostics         DiagnosticsPort
	jobPools            JobPoolStatusPort
	jobPoolControl      JobPoolControlPort
	slotSessions        SlotSessionControlPort
	environments        EnvironmentControlPort
	environmentExecutor EnvironmentExecutor
	jobPoolID           string
	maxConcurrency      int
	clock               func() time.Time
}

var _ coreapi.API = (*Service)(nil)
var _ coreapi.DiagnosticSnapshotAPI = (*Service)(nil)
var _ coreapi.JobPoolAPI = (*Service)(nil)
var _ coreapi.EnvironmentAPI = (*Service)(nil)

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Requests == nil || dependencies.Store == nil {
		return nil, ErrInvalidService
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{requests: dependencies.Requests, store: dependencies.Store, views: dependencies.Views, rdp: dependencies.RDP, diagnostics: dependencies.Diagnostics, jobPools: dependencies.JobPools, jobPoolControl: dependencies.JobPoolControl, slotSessions: dependencies.SlotSessions, environments: dependencies.Environments, environmentExecutor: dependencies.EnvironmentExecutor, jobPoolID: dependencies.JobPoolID, maxConcurrency: dependencies.MaxConcurrency, clock: clock}, nil
}

func (s *Service) StartSlotSession(ctx context.Context, input coreapi.StartSlotSessionRequest) (coreapi.SlotSessionOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.SlotSessionOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.SlotSessionOperation{}, err
	}
	if s.slotSessions == nil || (strings.TrimSpace(input.PoolID) == "") == (strings.TrimSpace(input.SlotID) == "") || !validToken(input.Actor) || !validToken(input.IdempotencyKey) || (input.PoolID != "" && !validToken(input.PoolID)) || (input.SlotID != "" && !validToken(input.SlotID)) {
		return coreapi.SlotSessionOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, idempotent, err := s.slotSessions.StartSlotSession(store.SlotSessionMutation{PoolID: input.PoolID, SlotID: input.SlotID, Actor: input.Actor, IdempotencyKey: input.IdempotencyKey, ExpectedRevision: input.ExpectedRevision, RequestedAt: s.clock().UTC()})
	if err != nil {
		return coreapi.SlotSessionOperation{}, classify(err)
	}
	return projectSlotSessionOperation(value, idempotent), nil
}

func (s *Service) GetSlotSessionOperation(ctx context.Context, operationID string) (coreapi.SlotSessionOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.SlotSessionOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.SlotSessionOperation{}, err
	}
	if s.slotSessions == nil || !validToken(operationID) {
		return coreapi.SlotSessionOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, err := s.slotSessions.GetSlotSessionOperation(operationID)
	if err != nil {
		return coreapi.SlotSessionOperation{}, classify(err)
	}
	return projectSlotSessionOperation(value, false), nil
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
	if !validToken(poolID) {
		return coreapi.JobPoolStatus{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	if s.jobPools == nil && s.jobPoolControl == nil {
		return coreapi.JobPoolStatus{}, coreapi.NewError(coreapi.CodeUnavailable, "job pool status is unavailable")
	}
	now := s.clock()
	if now.IsZero() {
		return coreapi.JobPoolStatus{}, coreapi.NewError(coreapi.CodeInternal, "job pool status is unavailable")
	}
	if s.jobPoolControl != nil {
		projection, err := s.jobPoolControl.GetJobPoolProjection(poolID, now)
		if err != nil {
			return coreapi.JobPoolStatus{}, classify(err)
		}
		return projectJobPoolStatus(projection), nil
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

func (s *Service) ListJobPools(ctx context.Context) ([]coreapi.JobPool, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if s.jobPoolControl == nil {
		return nil, coreapi.NewError(coreapi.CodeUnavailable, "job pool control is unavailable")
	}
	items, err := s.jobPoolControl.ListJobPoolProjections(s.clock())
	if err != nil {
		return nil, classify(err)
	}
	if len(items) > maxQueryLimit {
		items = items[:maxQueryLimit]
	}
	result := make([]coreapi.JobPool, 0, len(items))
	for _, item := range items {
		result = append(result, projectJobPool(item))
	}
	return result, nil
}

func (s *Service) GetJobPool(ctx context.Context, poolID string) (coreapi.JobPool, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPool{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPool{}, err
	}
	if !validToken(poolID) {
		return coreapi.JobPool{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	if s.jobPoolControl == nil {
		return coreapi.JobPool{}, coreapi.NewError(coreapi.CodeUnavailable, "job pool control is unavailable")
	}
	item, err := s.jobPoolControl.GetJobPoolProjection(poolID, s.clock())
	if err != nil {
		return coreapi.JobPool{}, classify(err)
	}
	return projectJobPool(item), nil
}

func (s *Service) ApplyJobPool(ctx context.Context, input coreapi.JobPoolApplyRequest) (coreapi.JobPoolOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if s.jobPoolControl == nil || !validToken(input.Config.PoolID) || !validToken(input.Config.EnvironmentID) || !validToken(input.IdempotencyKey) || !validToken(input.Actor) || input.Config.DesiredSlots < 0 || input.Config.MaxConcurrency < 0 {
		return coreapi.JobPoolOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, idempotent, err := s.jobPoolControl.ApplyJobPool(store.JobPoolMutation{Config: unprojectJobPoolConfig(input.Config), ExpectedRevision: input.ExpectedRevision, IdempotencyKey: input.IdempotencyKey, Actor: input.Actor, RequestedAt: input.RequestedAt})
	if err != nil {
		return coreapi.JobPoolOperation{}, classify(err)
	}
	if reconcileErr := s.reconcileJobPool(input.Config.PoolID); reconcileErr != nil {
		if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
			value = current
		}
		return projectJobPoolOperation(value, idempotent), reconcileErr
	}
	if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
		value = current
	}
	return projectJobPoolOperation(value, idempotent), nil
}

func (s *Service) ScaleJobPool(ctx context.Context, input coreapi.JobPoolScaleRequest) (coreapi.JobPoolOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if s.jobPoolControl == nil || !validToken(input.PoolID) || !validToken(input.IdempotencyKey) || !validToken(input.Actor) || input.DesiredSlots < 0 {
		return coreapi.JobPoolOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, idempotent, err := s.jobPoolControl.ScaleJobPool(input.PoolID, input.DesiredSlots, input.ExpectedRevision, input.IdempotencyKey, input.Actor, input.RequestedAt)
	if err != nil {
		return coreapi.JobPoolOperation{}, classify(err)
	}
	if reconcileErr := s.reconcileJobPool(input.PoolID); reconcileErr != nil {
		if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
			value = current
		}
		return projectJobPoolOperation(value, idempotent), reconcileErr
	}
	if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
		value = current
	}
	return projectJobPoolOperation(value, idempotent), nil
}

func (s *Service) DrainJobPool(ctx context.Context, input coreapi.JobPoolActionRequest) (coreapi.JobPoolOperation, error) {
	return s.jobPoolAction(ctx, input, true)
}

func (s *Service) ResumeJobPool(ctx context.Context, input coreapi.JobPoolActionRequest) (coreapi.JobPoolOperation, error) {
	return s.jobPoolAction(ctx, input, false)
}

func (s *Service) DeleteJobPool(ctx context.Context, input coreapi.JobPoolDeleteRequest) (coreapi.JobPoolOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if s.jobPoolControl == nil || !validToken(input.PoolID) || !validToken(input.IdempotencyKey) || !validToken(input.Actor) {
		return coreapi.JobPoolOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, idempotent, err := s.jobPoolControl.DeleteJobPool(input.PoolID, input.ExpectedRevision, input.IdempotencyKey, input.Actor, input.RequestedAt)
	if err != nil {
		return coreapi.JobPoolOperation{}, classify(err)
	}
	if reconcileErr := s.reconcileJobPool(input.PoolID); reconcileErr != nil {
		if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
			value = current
		}
		return projectJobPoolOperation(value, idempotent), reconcileErr
	}
	if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
		value = current
	}
	return projectJobPoolOperation(value, idempotent), nil
}

func (s *Service) jobPoolAction(ctx context.Context, input coreapi.JobPoolActionRequest, drain bool) (coreapi.JobPoolOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if s.jobPoolControl == nil || !validToken(input.PoolID) || !validToken(input.IdempotencyKey) || !validToken(input.Actor) {
		return coreapi.JobPoolOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	var value store.JobPoolOperation
	var idempotent bool
	var err error
	if drain {
		value, idempotent, err = s.jobPoolControl.DrainJobPool(input.PoolID, input.ExpectedRevision, input.IdempotencyKey, input.Actor, input.RequestedAt)
	} else {
		value, idempotent, err = s.jobPoolControl.ResumeJobPool(input.PoolID, input.ExpectedRevision, input.IdempotencyKey, input.Actor, input.RequestedAt)
	}
	if err != nil {
		return coreapi.JobPoolOperation{}, classify(err)
	}
	if reconcileErr := s.reconcileJobPool(input.PoolID); reconcileErr != nil {
		if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
			value = current
		}
		return projectJobPoolOperation(value, idempotent), reconcileErr
	}
	if current, getErr := s.jobPoolControl.GetJobPoolOperation(value.OperationID); getErr == nil {
		value = current
	}
	return projectJobPoolOperation(value, idempotent), nil
}

func (s *Service) reconcileJobPool(poolID string) error {
	_, err := s.jobPoolControl.ReconcileJobPoolControl(poolID, s.clock())
	if err != nil {
		return classify(err)
	}
	return nil
}

func (s *Service) GetJobPoolOperation(ctx context.Context, operationID string) (coreapi.JobPoolOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.JobPoolOperation{}, err
	}
	if s.jobPoolControl == nil || !validToken(operationID) {
		return coreapi.JobPoolOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, err := s.jobPoolControl.GetJobPoolOperation(operationID)
	if err != nil {
		return coreapi.JobPoolOperation{}, classify(err)
	}
	return projectJobPoolOperation(value, false), nil
}

func (s *Service) ListEnvironments(ctx context.Context) ([]coreapi.Environment, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if s.environments == nil {
		return nil, coreapi.NewError(coreapi.CodeUnavailable, "environment control is unavailable")
	}
	items, err := s.environments.ListEnvironmentRecords()
	if err != nil {
		return nil, classify(err)
	}
	if len(items) > maxQueryLimit {
		items = items[:maxQueryLimit]
	}
	result := make([]coreapi.Environment, 0, len(items))
	for _, item := range items {
		result = append(result, projectEnvironment(item))
	}
	return result, nil
}

func (s *Service) EnvironmentOperation(ctx context.Context, input coreapi.EnvironmentOperationRequest) (coreapi.EnvironmentOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.EnvironmentOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.EnvironmentOperation{}, err
	}
	if s.environments == nil || !validToken(input.EnvironmentID) || !validToken(input.Version) || !validToken(input.Operation) || !validToken(input.IdempotencyKey) || !validToken(input.Actor) || (input.PackageRef != "" && (!validToken(input.PackageRef) || strings.ContainsAny(input.PackageRef, "/\\"))) || !validEnvironmentOperation(input.Operation) {
		return coreapi.EnvironmentOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	mutation := store.EnvironmentMutation{EnvironmentID: input.EnvironmentID, Version: input.Version, Operation: input.Operation, PackageRef: input.PackageRef, ExpectedRevision: input.ExpectedRevision, IdempotencyKey: input.IdempotencyKey, Actor: input.Actor, RequestedAt: input.RequestedAt}
	value, idempotent, err := s.environments.ApplyEnvironmentOperation(mutation)
	if err != nil {
		return coreapi.EnvironmentOperation{}, classify(err)
	}
	if idempotent {
		return projectEnvironmentOperation(value, true), nil
	}
	state, failure := "applied", ""
	if input.Operation == "install" || input.Operation == "upgrade" || input.Operation == "rollback" {
		if s.environmentExecutor == nil {
			state, failure = "failed", "package_unavailable"
		} else {
			if _, updateErr := s.environments.UpdateEnvironmentOperation(value.OperationID, "provisioning", "", s.clock()); updateErr != nil {
				return coreapi.EnvironmentOperation{}, classify(updateErr)
			}
			var record environment.Record
			if executed, executeErr := s.environmentExecutor.Execute(ctx, mutation); executeErr != nil {
				state, failure = "failed", environmentFailureCode(executeErr)
			} else {
				record = executed
				if finalizer, ok := s.environments.(EnvironmentOperationFinalizer); ok {
					updated, finalizeErr := finalizer.CompleteEnvironmentOperation(value.OperationID, state, failure, record, s.clock())
					if finalizeErr != nil {
						return coreapi.EnvironmentOperation{}, classify(finalizeErr)
					}
					return projectEnvironmentOperation(updated, false), nil
				}
			}
		}
	} else {
		record, gateErr := s.environments.ApplyEnvironmentGate(input.EnvironmentID, input.Version, input.Operation, s.clock())
		if gateErr != nil {
			state, failure = "failed", environmentFailureCode(gateErr)
		} else if finalizer, ok := s.environments.(EnvironmentOperationFinalizer); ok {
			updated, finalizeErr := finalizer.CompleteEnvironmentOperation(value.OperationID, state, failure, record, s.clock())
			if finalizeErr != nil {
				return coreapi.EnvironmentOperation{}, classify(finalizeErr)
			}
			return projectEnvironmentOperation(updated, false), nil
		}
	}
	updated, updateErr := s.environments.UpdateEnvironmentOperation(value.OperationID, state, failure, s.clock())
	if updateErr != nil {
		return coreapi.EnvironmentOperation{}, classify(updateErr)
	}
	value = updated
	return projectEnvironmentOperation(value, false), nil
}

func (s *Service) GetEnvironmentOperation(ctx context.Context, operationID string) (coreapi.EnvironmentOperation, error) {
	if err := s.ready(); err != nil {
		return coreapi.EnvironmentOperation{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.EnvironmentOperation{}, err
	}
	if s.environments == nil || !validToken(operationID) {
		return coreapi.EnvironmentOperation{}, coreapi.NewError(coreapi.CodeInvalidArgument, "request is invalid")
	}
	value, err := s.environments.GetEnvironmentOperation(operationID)
	if err != nil {
		return coreapi.EnvironmentOperation{}, classify(err)
	}
	return projectEnvironmentOperation(value, false), nil
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
	status, err := s.diagnostics.Submit(ctx, diagnostics.ReportInput{Severity: input.Severity, Category: input.Category, Summary: input.Summary, ErrorClass: input.ErrorClass, Operation: input.Operation})
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

func (s *Service) GetDiagnosticSnapshot(ctx context.Context, input coreapi.DiagnosticSnapshotRequest) (coreapi.DiagnosticSnapshot, error) {
	if err := s.ready(); err != nil {
		return coreapi.DiagnosticSnapshot{}, err
	}
	if err := checkContext(ctx); err != nil {
		return coreapi.DiagnosticSnapshot{}, err
	}
	port, ok := s.diagnostics.(DiagnosticSnapshotPort)
	if !ok {
		return coreapi.DiagnosticSnapshot{}, coreapi.NewError(coreapi.CodeUnavailable, "diagnostic snapshot is unavailable")
	}
	report, err := port.Snapshot(ctx, diagnostics.ReportInput{Severity: input.Severity, Category: input.Category, Summary: input.Summary, ErrorClass: input.ErrorClass, Operation: input.Operation})
	if err != nil {
		if errors.Is(err, diagnostics.ErrInvalidReport) {
			return coreapi.DiagnosticSnapshot{}, classify(requestservice.ErrInvalidInput)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return coreapi.DiagnosticSnapshot{}, classify(err)
		}
		return coreapi.DiagnosticSnapshot{}, coreapi.NewError(coreapi.CodeInternal, "diagnostic snapshot failed")
	}
	events := make([]coreapi.DiagnosticEvent, 0, len(report.Events))
	for _, event := range report.Events {
		event = observability.SanitizeEvent(event, report.CreatedAt)
		events = append(events, coreapi.DiagnosticEvent{
			At: event.At, Component: event.Component, Operation: event.Operation,
			Outcome: event.Outcome, RequestID: event.RequestID,
			Resource: event.Resource, ErrorClass: event.ErrorClass,
			DurationMS: durationMilliseconds(event.Duration),
		})
	}
	schema := report.Schema
	if schema == "" {
		schema = "chuzi.diagnostic/v2"
	}
	source := report.Source
	if source == "" {
		source = "core"
	}
	eventCount := report.EventCount
	if eventCount < len(events) {
		eventCount = len(events)
	}
	return coreapi.DiagnosticSnapshot{
		Schema: schema, ID: report.ID, CreatedAt: report.CreatedAt, Source: source,
		Version: report.Version, Platform: report.Platform, Arch: report.Arch,
		Severity: report.Severity, Category: report.Category, Summary: report.Summary,
		ErrorClass: report.ErrorClass, Operation: report.Operation, EventCount: eventCount,
		EventsTruncated: report.EventsTruncated, CaptureErrorClass: report.CaptureErrorClass,
		CoreStatus: coreapi.DiagnosticCoreStatus{Installed: true, Running: true, Ready: true, Status: "ready"},
		Events:     events,
	}, nil
}

func durationMilliseconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	value := duration.Milliseconds()
	if value < 1 {
		return 1
	}
	return value
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

func projectJobPoolConfig(value slot.PoolConfig) coreapi.JobPoolConfig {
	value = value.Normalized()
	return coreapi.JobPoolConfig{PoolID: value.PoolID, DesiredSlots: value.DesiredSlots, MaxConcurrency: value.MaxConcurrency, EnvironmentID: value.EnvironmentID, EnvironmentVersion: value.EnvironmentVersion, ManifestDigest: value.ManifestDigest, Signer: value.Signer, Capabilities: append([]string(nil), value.Capabilities...), RequireTrusted: value.RequireTrusted, DesiredState: value.DesiredState, Enabled: value.Enabled(), ConfigRevision: value.ConfigRevision, UpdatedAt: value.UpdatedAt, UpdatedBy: observability.RedactIdentifier(value.UpdatedBy)}
}

func projectJobPoolStatus(value store.JobPoolProjection) coreapi.JobPoolStatus {
	status := value.Status
	max := value.Config.MaxConcurrency
	effective := status.Ready
	if max > 0 && effective > max {
		effective = max
	}
	return coreapi.JobPoolStatus{PoolID: status.PoolID, EnvironmentID: status.EnvironmentID, EnvironmentVersion: status.EnvironmentVersion, Desired: status.Desired, Ready: status.Ready, Leased: status.Leased, Quarantined: status.Quarantined, Draining: status.Draining, Provisioning: status.Provisioning, Retiring: status.Retiring, Unprovisioned: status.Unprovisioned, EffectiveCapacity: effective, MaxConcurrency: max, DesiredState: value.Config.DesiredState, Enabled: value.Config.Enabled(), EnvironmentReady: value.EnvironmentReady, EnvironmentReadiness: value.EnvironmentReadiness, ReconcileState: string(value.ReconcileState), OperationID: value.OperationID, LastFailureCode: value.LastFailureCode, LastSuccessfulReconcileAt: value.LastSuccessfulReconcileAt, ConfigRevision: value.Config.ConfigRevision}
}

func projectJobPool(value store.JobPoolProjection) coreapi.JobPool {
	return coreapi.JobPool{Config: projectJobPoolConfig(value.Config), Status: projectJobPoolStatus(value)}
}

func unprojectJobPoolConfig(value coreapi.JobPoolConfig) slot.PoolConfig {
	state := value.DesiredState
	if state == "" {
		state = "enabled"
	}
	return slot.PoolConfig{PoolID: value.PoolID, DesiredSlots: value.DesiredSlots, MaxConcurrency: value.MaxConcurrency, EnvironmentID: value.EnvironmentID, EnvironmentVersion: value.EnvironmentVersion, ManifestDigest: value.ManifestDigest, Signer: value.Signer, Capabilities: append([]string(nil), value.Capabilities...), RequireTrusted: value.RequireTrusted, DesiredState: state}
}

func projectJobPoolOperation(value store.JobPoolOperation, idempotent bool) coreapi.JobPoolOperation {
	return coreapi.JobPoolOperation{OperationID: value.OperationID, PoolID: value.PoolID, Operation: value.Operation, State: string(value.State), Actor: observability.RedactIdentifier(value.Actor), ConfigRevision: value.ConfigRevision, RequestedAt: value.RequestedAt, UpdatedAt: value.UpdatedAt, CompletedAt: value.CompletedAt, Result: value.Result, FailureCode: value.FailureCode, EnvironmentGeneration: value.EnvironmentGeneration, LastSuccessfulAt: value.LastSuccessfulAt, Idempotent: idempotent}
}

func projectSlotSessionOperation(value store.SlotSessionOperation, idempotent bool) coreapi.SlotSessionOperation {
	status := coreapi.SlotSessionStatus{PoolID: value.PoolID, SlotID: value.SlotID, Ordinal: value.Ordinal, Status: value.SessionState, EnvironmentGeneration: value.EnvironmentGeneration, SessionState: value.SessionState, AgentReady: value.AgentReady}
	return coreapi.SlotSessionOperation{OperationID: value.OperationID, PoolID: value.PoolID, SlotID: value.SlotID, Ordinal: value.Ordinal, State: string(value.State), Actor: observability.RedactIdentifier(value.Actor), RequestedAt: value.RequestedAt, UpdatedAt: value.UpdatedAt, CompletedAt: value.CompletedAt, FailureCode: value.FailureCode, EnvironmentGeneration: value.EnvironmentGeneration, Status: status, Idempotent: idempotent}
}

func projectEnvironment(value environment.Record) coreapi.Environment {
	return coreapi.Environment{EnvironmentID: value.EnvironmentID, Version: value.Version, Capabilities: append([]string(nil), value.Capabilities...), ManifestDigest: value.ManifestDigest, Signer: value.Signer, Installed: value.Installed, Verified: value.Verified, Trusted: value.Trusted, Enabled: value.Enabled, Healthy: value.Healthy, Ready: value.Ready, Generation: value.Generation, UpdatedAt: value.UpdatedAt}
}

func projectEnvironmentOperation(value store.EnvironmentOperationRecord, idempotent bool) coreapi.EnvironmentOperation {
	return coreapi.EnvironmentOperation{OperationID: value.OperationID, EnvironmentID: value.EnvironmentID, Version: value.Version, Operation: value.Operation, State: value.State, FailureCode: value.FailureCode, EnvironmentGeneration: value.EnvironmentGeneration, RequestedAt: value.RequestedAt, UpdatedAt: value.UpdatedAt, Idempotent: idempotent}
}

func environmentFailureCode(err error) string {
	switch {
	case errors.Is(err, environment.ErrPackageReference), errors.Is(err, environment.ErrNotInstalled), errors.Is(err, environment.ErrRollbackUnavailable):
		return "package_unavailable"
	case errors.Is(err, environment.ErrExternalModification):
		return "environment_untrusted"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, environment.ErrNotTrusted):
		return "environment_untrusted"
	case errors.Is(err, environment.ErrNotVerified):
		return "environment_unverified"
	case errors.Is(err, environment.ErrNotHealthy):
		return "environment_unhealthy"
	case errors.Is(err, environment.ErrNotInstalled):
		return "package_unavailable"
	default:
		return "environment_operation_failed"
	}
}

func validEnvironmentOperation(value string) bool {
	switch value {
	case "install", "upgrade", "verify", "trust", "enable", "disable", "health", "rollback":
		return true
	default:
		return false
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
		errors.Is(err, slot.ErrInvalidConfig), errors.Is(err, slot.ErrInvalidRequest),
		errors.Is(err, environment.ErrInvalidManifest),
		errors.Is(err, account.ErrInvalidSnapshot):
		code = coreapi.CodeInvalidArgument
	case errors.Is(err, requestservice.ErrNotAllowed):
		code = coreapi.CodeForbidden
	case errors.Is(err, requestservice.ErrRateLimited):
		code = coreapi.CodeRateLimited
	case errors.Is(err, store.ErrAccountNotFound), errors.Is(err, store.ErrRequestNotFound),
		errors.Is(err, store.ErrLeaseNotFound), errors.Is(err, store.ErrJobPoolNotFound), errors.Is(err, store.ErrJobPoolOperationNotFound), errors.Is(err, store.ErrEnvironmentOperationNotFound), errors.Is(err, store.ErrSlotSessionOperationNotFound), errors.Is(err, slot.ErrPoolNotFound), errors.Is(err, slot.ErrSlotNotFound):
		code = coreapi.CodeNotFound
	case errors.Is(err, store.ErrAccountExists), errors.Is(err, store.ErrRequestExists),
		errors.Is(err, store.ErrRequestConflict), errors.Is(err, store.ErrIdempotencyConflict),
		errors.Is(err, store.ErrJobPoolStaleRevision), errors.Is(err, store.ErrJobPoolConflict), errors.Is(err, store.ErrJobPoolIdempotencyConflict), errors.Is(err, store.ErrSlotSessionRevision), errors.Is(err, store.ErrSlotSessionIdempotencyConflict),
		errors.Is(err, store.ErrEnvironmentIdempotencyConflict), errors.Is(err, store.ErrEnvironmentStaleRevision),
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
