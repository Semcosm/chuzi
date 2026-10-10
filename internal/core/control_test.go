package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/store"
)

var controlTestTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

type controlJobPools struct {
	projection store.JobPoolProjection
	err        error
}

type controlSlotSessions struct {
	operation   store.SlotSessionOperation
	err         error
	calls       int
	mutation    store.SlotSessionMutation
	idempotent  bool
	operationID string
}

func (p *controlSlotSessions) StartSlotSession(mutation store.SlotSessionMutation) (store.SlotSessionOperation, bool, error) {
	p.calls++
	p.mutation = mutation
	return p.operation, p.idempotent, p.err
}
func (p *controlSlotSessions) GetSlotSessionOperation(operationID string) (store.SlotSessionOperation, error) {
	p.operationID = operationID
	return p.operation, p.err
}

func (p controlJobPools) ListJobPoolProjections(time.Time) ([]store.JobPoolProjection, error) {
	if p.err != nil {
		return nil, p.err
	}
	return []store.JobPoolProjection{p.projection}, nil
}
func (p controlJobPools) GetJobPoolProjection(string, time.Time) (store.JobPoolProjection, error) {
	return p.projection, p.err
}
func (p controlJobPools) ApplyJobPool(store.JobPoolMutation) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) ScaleJobPool(string, int, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) DrainJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) ResumeJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) DeleteJobPool(string, uint64, string, string, time.Time) (store.JobPoolOperation, bool, error) {
	return store.JobPoolOperation{}, false, p.err
}
func (p controlJobPools) GetJobPoolOperation(string) (store.JobPoolOperation, error) {
	return store.JobPoolOperation{}, p.err
}
func (p controlJobPools) ReconcileJobPoolControl(string, time.Time) (store.JobPoolProjection, error) {
	return p.projection, p.err
}

type controlEnvironments struct {
	record environment.Record
	err    error
}

type recordingEnvironmentExecutor struct {
	mutation store.EnvironmentMutation
	err      error
}

func (e *recordingEnvironmentExecutor) Execute(_ context.Context, mutation store.EnvironmentMutation) (environment.Record, error) {
	e.mutation = mutation
	return environment.Record{EnvironmentID: mutation.EnvironmentID, Version: mutation.Version, ManifestDigest: strings.Repeat("a", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: controlTestTime}, e.err
}

func (e controlEnvironments) ListEnvironmentRecords() ([]environment.Record, error) {
	return []environment.Record{e.record}, e.err
}
func (e controlEnvironments) GetEnvironmentRecord(string, string) (environment.Record, error) {
	if e.err != nil {
		return environment.Record{}, e.err
	}
	return e.record, nil
}
func (e controlEnvironments) ApplyEnvironmentOperation(store.EnvironmentMutation) (store.EnvironmentOperationRecord, bool, error) {
	return store.EnvironmentOperationRecord{OperationID: "envop-1", EnvironmentID: "env/v1", Version: "1.0.0", Operation: "trust", State: "requested", RequestedAt: controlTestTime, UpdatedAt: controlTestTime}, false, e.err
}
func (e controlEnvironments) GetEnvironmentOperation(string) (store.EnvironmentOperationRecord, error) {
	return store.EnvironmentOperationRecord{}, e.err
}
func (e controlEnvironments) UpdateEnvironmentOperation(string, string, string, time.Time) (store.EnvironmentOperationRecord, error) {
	return store.EnvironmentOperationRecord{OperationID: "envop-1", EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", State: "applied", RequestedAt: controlTestTime, UpdatedAt: controlTestTime}, e.err
}
func (e controlEnvironments) ApplyEnvironmentGate(string, string, string, time.Time) (environment.Record, error) {
	return e.record, e.err
}

func TestJobPoolProjectionRedactsActorAndPreservesCapacityFields(t *testing.T) {
	projection := store.JobPoolProjection{
		Config:           slot.PoolConfig{PoolID: "pool-1", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0", DesiredSlots: 4, MaxConcurrency: 2, ConfigRevision: 3, UpdatedBy: "operator@example", DesiredState: "enabled"},
		Status:           slot.StatusCounts{PoolID: "pool-1", Desired: 4, Ready: 3, Leased: 1, Quarantined: 1, Draining: 1, Provisioning: 1, Retiring: 1, Unprovisioned: 1},
		EnvironmentReady: true, EnvironmentReadiness: "ready", ReconcileState: store.JobPoolApplied, OperationID: "op-1",
	}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{projection: projection}, Clock: func() time.Time { return controlTestTime }})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListJobPools(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, err=%v", items, err)
	}
	item := items[0]
	if item.Status.EffectiveCapacity != 2 || item.Config.UpdatedBy == "operator@example" || !strings.HasPrefix(item.Config.UpdatedBy, "id_") {
		t.Fatalf("projection = %#v", item)
	}
}

func TestJobPoolStatusRejectsInvalidPoolIDAndMapsStaleRevision(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{err: store.ErrJobPoolStaleRevision}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetJobPoolStatus(context.Background(), "bad pool"); coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("invalid pool error = %v, code=%q", err, coreapi.CodeOf(err))
	}
	_, err = service.ScaleJobPool(context.Background(), coreapi.JobPoolScaleRequest{PoolID: "pool-1", DesiredSlots: 2, IdempotencyKey: "idem", Actor: "operator"})
	if coreapi.CodeOf(err) != coreapi.CodeConflict || errors.Is(err, store.ErrJobPoolStaleRevision) {
		t.Fatalf("stale revision error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestEnvironmentOperationRejectsUncontrolledPackageRef(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Environments: controlEnvironments{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.EnvironmentOperation(context.Background(), coreapi.EnvironmentOperationRequest{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", PackageRef: "../package", IdempotencyKey: "idem", Actor: "operator"})
	if coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("package ref error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestEnvironmentPackageOperationUsesControlledExecutor(t *testing.T) {
	executor := &recordingEnvironmentExecutor{}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, Environments: controlEnvironments{}, EnvironmentExecutor: executor})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := service.EnvironmentOperation(context.Background(), coreapi.EnvironmentOperationRequest{EnvironmentID: "env/v1", Version: "1.0.0", Operation: "install", PackageRef: "catalog-v1", IdempotencyKey: "idem", Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "applied" || executor.mutation.PackageRef != "catalog-v1" || executor.mutation.Operation != "install" {
		t.Fatalf("operation = %#v, mutation = %#v", operation, executor.mutation)
	}
}

func TestApplyJobPoolClassifiesInvalidConfiguration(t *testing.T) {
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{err: slot.ErrInvalidConfig}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyJobPool(context.Background(), coreapi.JobPoolApplyRequest{
		Config:         coreapi.JobPoolConfig{PoolID: "pool-1", EnvironmentID: "env/v1", DesiredState: "unknown"},
		IdempotencyKey: "idem", Actor: "operator",
	})
	if coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("invalid pool configuration error = %v, code=%q", err, coreapi.CodeOf(err))
	}
}

func TestApplyJobPoolRejectsUnreadyOrMismatchedTrustedEnvironment(t *testing.T) {
	ready := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: strings.Repeat("a", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: controlTestTime}
	for _, test := range []struct {
		name   string
		record environment.Record
		digest string
		signer string
		code   coreapi.Code
	}{
		{name: "missing", code: coreapi.CodeUnavailable},
		{name: "unready", record: func() environment.Record { value := ready; value.Ready = false; return value }(), code: coreapi.CodeUnavailable},
		{name: "digest mismatch", record: ready, digest: strings.Repeat("b", 64), code: coreapi.CodeInvalidArgument},
		{name: "signer mismatch", record: ready, signer: "other-signer", code: coreapi.CodeInvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{}, Environments: controlEnvironments{record: test.record}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.ApplyJobPool(context.Background(), coreapi.JobPoolApplyRequest{Config: coreapi.JobPoolConfig{PoolID: "pool-1", EnvironmentID: ready.EnvironmentID, EnvironmentVersion: ready.Version, ManifestDigest: test.digest, Signer: test.signer, RequireTrusted: true}, IdempotencyKey: "apply-1", Actor: "operator"})
			if coreapi.CodeOf(err) != test.code {
				t.Fatalf("error = %v, code = %q", err, coreapi.CodeOf(err))
			}
		})
	}
}

func TestWindowsPoolApplyCannotDisableTrust(t *testing.T) {
	service, err := New(Dependencies{ExecutionMode: "windows", Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyJobPool(context.Background(), coreapi.JobPoolApplyRequest{Config: coreapi.JobPoolConfig{PoolID: "pool-1", EnvironmentID: "env/v1", EnvironmentVersion: "1.0.0"}, IdempotencyKey: "apply-1", Actor: "operator"})
	if coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
		t.Fatalf("unsigned Windows pool apply = %v", err)
	}
}

func TestWindowsPoolApplyToZeroPreservesBindingAndWaitsForCleanup(t *testing.T) {
	for _, failure := range []string{"unhealthy", "disabled", "untrusted", "unverified", "missing environment port"} {
		t.Run(failure, func(t *testing.T) {
			cfg, err := config.New(filepath.Join(t.TempDir(), "data"))
			if err != nil {
				t.Fatal(err)
			}
			database, err := store.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			record := environment.Record{EnvironmentID: "env/v1", Version: "1.0.0", ManifestDigest: strings.Repeat("a", 64), Signer: "signer", Installed: true, Verified: true, Trusted: true, Enabled: true, Healthy: true, Ready: true, Generation: 1, UpdatedAt: controlTestTime}
			if err := database.PutEnvironmentRecord(record); err != nil {
				t.Fatal(err)
			}
			service, err := New(Dependencies{ExecutionMode: "windows", Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: database, Environments: database, Clock: func() time.Time { return controlTestTime }})
			if err != nil {
				t.Fatal(err)
			}
			input := coreapi.JobPoolApplyRequest{Config: coreapi.JobPoolConfig{PoolID: "pool-1", EnvironmentID: record.EnvironmentID, EnvironmentVersion: record.Version, DesiredSlots: 1, RequireTrusted: true}, IdempotencyKey: "initial", Actor: "operator", RequestedAt: controlTestTime}
			if _, err := service.ApplyJobPool(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			if err := database.MarkSlotReady("pool-1-001", slot.EnvironmentSummary{EnvironmentID: record.EnvironmentID, Version: record.Version, ManifestDigest: record.ManifestDigest, Signer: record.Signer, Trusted: true, Generation: 1, UpdatedAt: controlTestTime}, controlTestTime); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ReconcileJobPoolControl("pool-1", controlTestTime); err != nil {
				t.Fatal(err)
			}
			record.Ready = false
			switch failure {
			case "unhealthy", "missing environment port":
				record.Healthy = false
			case "disabled":
				record.Enabled = false
			case "untrusted":
				record.Trusted, record.Enabled, record.Healthy = false, false, false
			case "unverified":
				record.Verified, record.Trusted, record.Enabled, record.Healthy = false, false, false, false
			}
			if err := database.PutEnvironmentRecord(record); err != nil {
				t.Fatal(err)
			}
			if failure == "missing environment port" {
				service.environments = nil
			}
			input.ExpectedRevision, input.IdempotencyKey = 1, "blocked"
			if _, err := service.ApplyJobPool(context.Background(), input); coreapi.CodeOf(err) != coreapi.CodeUnavailable {
				t.Fatalf("unready capacity apply: %v", err)
			}
			input.Config.DesiredSlots = 0
			for _, changed := range []string{"digest", "signer", "version", "pool", "revision"} {
				rejected := input
				code := coreapi.CodeInvalidArgument
				switch changed {
				case "digest":
					rejected.Config.ManifestDigest = strings.Repeat("b", 64)
				case "signer":
					rejected.Config.Signer = "other-signer"
				case "version":
					rejected.Config.EnvironmentVersion, code = "2.0.0", coreapi.CodeUnavailable
				case "pool":
					rejected.Config.PoolID, rejected.ExpectedRevision, code = "new-pool", 0, coreapi.CodeUnavailable
				case "revision":
					rejected.ExpectedRevision, code = 0, coreapi.CodeConflict
				}
				if _, err := service.ApplyJobPool(context.Background(), rejected); coreapi.CodeOf(err) != code {
					t.Fatalf("cleanup with changed %s: %v", changed, err)
				}
			}
			input.IdempotencyKey = "cleanup"
			operation, err := service.ApplyJobPool(context.Background(), input)
			if err != nil || operation.State != string(store.JobPoolProvisioning) {
				t.Fatalf("cleanup apply: %#v %v", operation, err)
			}
			projection, err := database.GetJobPoolProjection("pool-1", controlTestTime)
			if err != nil || projection.Config.DesiredSlots != 0 || projection.Config.ManifestDigest != record.ManifestDigest || projection.Config.Signer != record.Signer || !projection.Config.RequireTrusted || projection.Status.Retiring != 1 || projection.EnvironmentReady {
				t.Fatalf("cleanup projection: %#v %v", projection, err)
			}
			duplicate, err := service.ApplyJobPool(context.Background(), input)
			if err != nil || !duplicate.Idempotent || duplicate.OperationID != operation.OperationID {
				t.Fatalf("cleanup retry: %#v %v", duplicate, err)
			}
			if err := database.SetSlotStatus("pool-1-001", slot.Deleted, controlTestTime); err != nil {
				t.Fatal(err)
			}
			projection, err = database.ReconcileJobPoolControl("pool-1", controlTestTime)
			if err != nil || projection.ReconcileState != store.JobPoolApplied || projection.Status.Retiring != 0 || projection.EnvironmentReady {
				t.Fatalf("completed cleanup: %#v %v", projection, err)
			}
		})
	}
}

func TestStartSlotSessionUsesRedactedAsyncOperation(t *testing.T) {
	port := &controlSlotSessions{operation: store.SlotSessionOperation{OperationID: "slotop-1", PoolID: "pool-1", SlotID: "pool-1-001", Ordinal: 1, State: store.SlotSessionRequested, Actor: "operator@example", RequestedAt: controlTestTime, UpdatedAt: controlTestTime, EnvironmentGeneration: 2, SessionState: "provisioning"}}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, SlotSessions: port, Clock: func() time.Time { return controlTestTime }})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := service.StartSlotSession(context.Background(), coreapi.StartSlotSessionRequest{PoolID: "pool-1", Actor: "operator@example", IdempotencyKey: "key-1"})
	if err != nil || operation.OperationID != "slotop-1" || operation.Status.SlotID != "pool-1-001" || operation.Status.AgentReady {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	if strings.Contains(operation.Actor, "operator@example") || strings.Contains(operation.Status.SlotID, "password") {
		t.Fatalf("operation leaked sensitive identity: %#v", operation)
	}
	if port.calls != 1 {
		t.Fatalf("calls=%d", port.calls)
	}
	if port.mutation.PoolID != "pool-1" || !port.mutation.RequestedAt.Equal(controlTestTime) {
		t.Fatalf("mutation=%#v", port.mutation)
	}
}

func TestStartSlotSessionRejectsInvalidSelectors(t *testing.T) {
	port := &controlSlotSessions{}
	service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, SlotSessions: port})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []coreapi.StartSlotSessionRequest{
		{Actor: "operator", IdempotencyKey: "key"},
		{PoolID: "pool-1", SlotID: "pool-1-001", Actor: "operator", IdempotencyKey: "key"},
		{PoolID: "bad pool", Actor: "operator", IdempotencyKey: "key"},
	} {
		if _, err := service.StartSlotSession(context.Background(), input); coreapi.CodeOf(err) != coreapi.CodeInvalidArgument {
			t.Fatalf("input=%#v err=%v code=%q", input, err, coreapi.CodeOf(err))
		}
	}
	if port.calls != 0 {
		t.Fatalf("invalid inputs reached store: %d", port.calls)
	}
}

func TestSlotSessionOperationErrorsUseStableCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code coreapi.Code
	}{
		{name: "stale revision", err: store.ErrSlotSessionRevision, code: coreapi.CodeConflict},
		{name: "idempotency conflict", err: store.ErrSlotSessionIdempotencyConflict, code: coreapi.CodeConflict},
		{name: "missing operation", err: store.ErrSlotSessionOperationNotFound, code: coreapi.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &controlSlotSessions{err: tc.err}
			service, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, SlotSessions: port})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing operation" {
				_, err = service.GetSlotSessionOperation(context.Background(), "slotop-1")
			} else {
				_, err = service.StartSlotSession(context.Background(), coreapi.StartSlotSessionRequest{PoolID: "pool-1", Actor: "operator", IdempotencyKey: "key"})
			}
			if coreapi.CodeOf(err) != tc.code {
				t.Fatalf("err=%v code=%q want=%q", err, coreapi.CodeOf(err), tc.code)
			}
		})
	}
}

type currentControlSlotSessions struct {
	controlSlotSessions
	current slot.Slot
	missing bool
}

func (p *currentControlSlotSessions) GetSlot(string) (slot.Slot, error) {
	if p.missing {
		return slot.Slot{}, slot.ErrSlotNotFound
	}
	return p.current, nil
}

func TestSlotSessionRefreshUsesCurrentRuntimeFacts(t *testing.T) {
	for _, mode := range []string{"logical", "windows", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			port := &currentControlSlotSessions{controlSlotSessions: controlSlotSessions{operation: store.SlotSessionOperation{OperationID: "op-a", SlotID: "slot-a", PoolID: "pool-a", State: store.SlotSessionReady, EnvironmentGeneration: 7, SessionState: "ready", AgentReady: true}}, current: slot.Slot{SlotID: "slot-a", Status: slot.Ready, AgentHandle: "native-handle", HealthAt: controlTestTime, EnvironmentGeneration: 7}}
			svc, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, SlotSessions: port, ExecutionMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			result, err := svc.GetSlotSessionOperation(context.Background(), "op-a")
			if err != nil || result.Status.ExecutionMode != mode || result.Status.AgentReady != (mode == "windows") {
				t.Fatalf("projection: %#v %v", result, err)
			}
			port.current.EnvironmentGeneration = 8
			result, _ = svc.GetSlotSessionOperation(context.Background(), "op-a")
			if result.Status.EnvironmentGeneration != 8 || result.Status.AgentReady != (mode == "windows") {
				t.Fatalf("current generation not projected: %#v", result)
			}
			port.current.AgentHandle = "logical"
			result, _ = svc.GetSlotSessionOperation(context.Background(), "op-a")
			if result.Status.AgentReady {
				t.Fatal("synthetic handle treated as native")
			}
			port.current.Status = slot.Quarantined
			result, _ = svc.GetSlotSessionOperation(context.Background(), "op-a")
			if result.State != "ready" || result.Status.Status != "quarantined" || result.Status.AgentReady {
				t.Fatalf("historical ready hides current failure: %#v", result)
			}
			port.missing = true
			result, _ = svc.GetSlotSessionOperation(context.Background(), "op-a")
			if result.Status.Status != "unavailable" || result.Status.AgentReady || result.Status.EnvironmentGeneration != 0 {
				t.Fatalf("missing slot: %#v", result)
			}
		})
	}
}

func TestJobPoolModeReportsActiveCoreConfiguration(t *testing.T) {
	for _, mode := range []string{"logical", "windows", "invalid"} {
		projection := store.JobPoolProjection{Config: slot.PoolConfig{PoolID: "pool-a"}, Status: slot.StatusCounts{PoolID: "pool-a"}}
		svc, err := New(Dependencies{Requests: viewServiceRequests{}, Store: viewServiceStore{}, JobPoolControl: controlJobPools{projection: projection}, ExecutionMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		expected := mode
		if mode == "invalid" {
			expected = "unknown"
		}
		pools, err := svc.ListJobPools(context.Background())
		if err != nil || len(pools) != 1 || pools[0].Status.ExecutionMode != expected {
			t.Fatalf("list: %#v %v", pools, err)
		}
		pool, err := svc.GetJobPool(context.Background(), "pool-a")
		if err != nil || pool.Status.ExecutionMode != expected {
			t.Fatalf("get: %#v %v", pool, err)
		}
		status, err := svc.GetJobPoolStatus(context.Background(), "pool-a")
		if err != nil || status.ExecutionMode != expected {
			t.Fatalf("status: %#v %v", status, err)
		}
	}
}
