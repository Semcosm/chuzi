package slotwindows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func validBrokerRequest() SessionBrokerRequest {
	return SessionBrokerRequest{PoolID: "pool-a",
		Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart,
		SlotID: "slot-001", Ordinal: 1, Generation: 7,
		SID: "S-1-5-21-100-200-300-400", Owner: "scheduler-1",
	}
}

func TestSessionBrokerProtocolRejectsUnknownAndInvalidFields(t *testing.T) {
	cases := []struct {
		name string
		data string
		code SessionBrokerCode
	}{
		{"unknown field", `{"version":2,"operation":"start","pool_id":"pool-a","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1","password":"secret"}`, SessionBrokerInvalidRequest},
		{"invalid version", `{"version":1,"operation":"start","pool_id":"pool-a","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidVersion},
		{"unknown operation", `{"version":2,"operation":"restart","pool_id":"pool-a","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerUnknownOperation},
		{"empty slot", `{"version":2,"operation":"start","pool_id":"pool-a","slot_id":"","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"invalid sid", `{"version":2,"operation":"start","pool_id":"pool-a","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"not-a-sid","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"zero generation", `{"version":2,"operation":"start","pool_id":"pool-a","slot_id":"slot-001","ordinal":1,"generation":0,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"invalid ordinal", `{"version":2,"operation":"start","pool_id":"pool-a","slot_id":"slot-001","ordinal":257,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"stop without session", `{"version":2,"operation":"stop","pool_id":"pool-a","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeSessionBrokerRequest([]byte(tc.data))
			if err == nil {
				t.Fatal("invalid request accepted")
			}
			var expected error
			switch tc.code {
			case SessionBrokerInvalidVersion:
				expected = ErrSessionBrokerInvalidVersion
			case SessionBrokerUnknownOperation:
				expected = ErrSessionBrokerUnknownOperation
			default:
				expected = ErrSessionBrokerInvalidRequest
			}
			if !errors.Is(err, expected) {
				t.Fatalf("error %v does not match %v", err, expected)
			}
		})
	}
}

func TestSessionBrokerProtocolOmitsCredentialsAndRejectsTrailingJSON(t *testing.T) {
	data, err := EncodeSessionBrokerRequest(validBrokerRequest())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "secret") {
		t.Fatalf("credential appeared in protocol: %s", data)
	}
	if _, err := DecodeSessionBrokerRequest(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := DecodeSessionBrokerResponse([]byte(`{"version":2,"operation":"start","code":"made_up","session_id":1,"state":"active"}`)); !errors.Is(err, ErrSessionBrokerInvalidRequest) {
		t.Fatalf("unknown response code was accepted: %v", err)
	}
}

func TestSessionBrokerACLIsNarrowAndFixed(t *testing.T) {
	acl := SessionBrokerACL{ServiceSID: "S-1-5-18", BrokerSID: "S-1-5-21-100-200-300-400"}
	if err := acl.Validate(); err != nil || !acl.Allows(acl.ServiceSID) || !acl.Allows(acl.BrokerSID) || acl.Allows("S-1-1-0") {
		t.Fatalf("unexpected ACL result: err=%v service=%v broker=%v world=%v", err, acl.Allows(acl.ServiceSID), acl.Allows(acl.BrokerSID), acl.Allows("S-1-1-0"))
	}
	if (SessionBrokerACL{ServiceSID: "S-1-1-0", BrokerSID: acl.BrokerSID}).Validate() == nil {
		t.Fatal("broad ACL identity accepted")
	}
	if SessionBrokerPipeName != `\\.\pipe\chuzi-session-bootstrap-v1` {
		t.Fatalf("pipe name changed: %q", SessionBrokerPipeName)
	}
}

type brokerTestAdapter struct {
	mu        sync.Mutex
	nextID    uint32
	active    map[string]BootstrapSession
	starts    int
	stops     int
	keepAlive bool
}

func newBrokerTestAdapter() *brokerTestAdapter {
	return &brokerTestAdapter{nextID: 100, active: make(map[string]BootstrapSession)}
}

func (a *brokerTestAdapter) Start(_ context.Context, ownership SessionBrokerOwnership) (BootstrapSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts++
	a.nextID++
	session := BootstrapSession{ID: a.nextID, State: "active"}
	a.active[ownership.SID] = session
	return session, nil
}

func (a *brokerTestAdapter) Stop(_ context.Context, ownership SessionBrokerOwnership) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stops++
	if !a.keepAlive {
		delete(a.active, ownership.SID)
	}
	return nil
}

func (a *brokerTestAdapter) Find(_ context.Context, sid string) (BootstrapSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.active[sid]
	if !ok {
		return BootstrapSession{State: "logged_off"}, ErrSessionUnavailable
	}
	return session, nil
}

func TestSessionBrokerOwnershipAndIdempotency(t *testing.T) {
	adapter := newBrokerTestAdapter()
	broker := NewSessionBroker(adapter)
	request := validBrokerRequest()
	started := broker.Handle(context.Background(), request)
	if started.Code != SessionBrokerOK || started.SessionID == 0 {
		t.Fatalf("start failed: %+v", started)
	}
	duplicate := broker.Handle(context.Background(), request)
	if duplicate.Code != SessionBrokerOK || duplicate.SessionID != started.SessionID || adapter.starts != 1 {
		t.Fatalf("duplicate start was not idempotent: %+v starts=%d", duplicate, adapter.starts)
	}
	wrongOwner := request
	wrongOwner.Owner = "other-owner"
	if response := broker.Handle(context.Background(), wrongOwner); response.Code != SessionBrokerOwnershipMismatch {
		t.Fatalf("wrong owner accepted: %+v", response)
	}
	stale := request
	stale.Generation = request.Generation - 1
	if response := broker.Handle(context.Background(), stale); response.Code != SessionBrokerStaleGeneration {
		t.Fatalf("stale generation accepted: %+v", response)
	}
	stop := request
	stop.Operation, stop.SessionID = SessionBrokerStop, started.SessionID
	if response := broker.Handle(context.Background(), stop); response.Code != SessionBrokerOK {
		t.Fatalf("stop failed: %+v", response)
	}
	if response := broker.Handle(context.Background(), stop); response.Code != SessionBrokerOK {
		t.Fatalf("duplicate stop was not idempotent: %+v", response)
	}
}

func TestSessionBrokerSerializesConcurrentStarts(t *testing.T) {
	adapter := newBrokerTestAdapter()
	broker := NewSessionBroker(adapter)
	request := validBrokerRequest()
	responses := make(chan SessionBrokerResponse, 16)
	var group sync.WaitGroup
	for range cap(responses) {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- broker.Handle(context.Background(), request)
		}()
	}
	group.Wait()
	close(responses)
	var sessionID uint32
	for response := range responses {
		if response.Code != SessionBrokerOK || response.SessionID == 0 {
			t.Fatalf("concurrent start response=%+v", response)
		}
		if sessionID == 0 {
			sessionID = response.SessionID
		} else if response.SessionID != sessionID {
			t.Fatalf("sessions diverged: %d and %d", sessionID, response.SessionID)
		}
	}
	if adapter.starts != 1 {
		t.Fatalf("starts=%d want 1", adapter.starts)
	}
}

type slowBrokerAdapter struct {
	*brokerTestAdapter
	slowSlot string
	entered  chan struct{}
	release  chan struct{}
}

func (a *slowBrokerAdapter) Start(ctx context.Context, ownership SessionBrokerOwnership) (BootstrapSession, error) {
	if ownership.SlotID == a.slowSlot {
		select {
		case a.entered <- struct{}{}:
		default:
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return BootstrapSession{}, ctx.Err()
		}
	}
	return a.brokerTestAdapter.Start(ctx, ownership)
}

func TestSessionBrokerDoesNotBlockIndependentSlotsOnSlowLogin(t *testing.T) {
	first := validBrokerRequest()
	adapter := &slowBrokerAdapter{brokerTestAdapter: newBrokerTestAdapter(), slowSlot: first.SlotID, entered: make(chan struct{}, 1), release: make(chan struct{})}
	broker := NewSessionBroker(adapter)
	firstDone := make(chan SessionBrokerResponse, 1)
	go func() { firstDone <- broker.Handle(context.Background(), first) }()
	select {
	case <-adapter.entered:
	case <-time.After(time.Second):
		t.Fatal("slow login did not start")
	}

	second := first
	second.SlotID = "slot-002"
	second.SID = "S-1-5-21-1-2-3-5"
	secondCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	secondResponse := broker.Handle(secondCtx, second)
	if secondResponse.Code != SessionBrokerOK || secondResponse.SessionID == 0 {
		t.Fatalf("independent slot was blocked by slow login: %+v", secondResponse)
	}
	close(adapter.release)
	select {
	case response := <-firstDone:
		if response.Code != SessionBrokerOK || response.SessionID == 0 {
			t.Fatalf("slow slot failed after release: %+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("slow slot did not complete after release")
	}
}

func TestSessionBrokerRestartCannotAdoptUnknownSession(t *testing.T) {
	adapter := newBrokerTestAdapter()
	request := validBrokerRequest()
	first := NewSessionBroker(adapter)
	started := first.Handle(context.Background(), request)
	if started.Code != SessionBrokerOK {
		t.Fatalf("start failed: %+v", started)
	}
	second := NewSessionBroker(adapter)
	if response := second.Handle(context.Background(), request); response.Code != SessionBrokerOwnershipUnknown {
		t.Fatalf("restart adopted unknown session: %+v", response)
	}
	stop := request
	stop.Operation, stop.SessionID = SessionBrokerStop, started.SessionID
	if response := second.Handle(context.Background(), stop); response.Code != SessionBrokerOwnershipUnknown {
		t.Fatalf("restart stopped unknown session: %+v", response)
	}
}

type memorySessionBrokerJournal struct {
	mu    sync.Mutex
	state SessionBrokerJournalState
}

func (j *memorySessionBrokerJournal) Load(context.Context) (SessionBrokerJournalState, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, nil
}

func (j *memorySessionBrokerJournal) Save(_ context.Context, state SessionBrokerJournalState) error {
	j.mu.Lock()
	j.state = state
	j.mu.Unlock()
	return nil
}

func TestSessionBrokerJournalRecoversOwnedSessionAfterRestart(t *testing.T) {
	adapter := newBrokerTestAdapter()
	journal := &memorySessionBrokerJournal{state: SessionBrokerJournalState{Version: SessionBrokerProtocolVersion}}
	first, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	request := validBrokerRequest()
	started := first.Handle(context.Background(), request)
	if started.Code != SessionBrokerOK {
		t.Fatalf("start failed: %+v", started)
	}
	second, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := second.Handle(context.Background(), request)
	if duplicate.Code != SessionBrokerOK || duplicate.SessionID != started.SessionID || adapter.starts != 1 {
		t.Fatalf("journal recovery was not idempotent: %+v starts=%d", duplicate, adapter.starts)
	}
	stop := request
	stop.Operation, stop.SessionID = SessionBrokerStop, started.SessionID
	if response := second.Handle(context.Background(), stop); response.Code != SessionBrokerOK {
		t.Fatalf("recovered stop failed: %+v", response)
	}
	third, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	if response := third.Handle(context.Background(), stop); response.Code != SessionBrokerOK {
		t.Fatalf("durable tombstone was not idempotent: %+v", response)
	}
}

func TestSessionBrokerJournalRejectsSessionIDReuse(t *testing.T) {
	adapter := newBrokerTestAdapter()
	journal := &memorySessionBrokerJournal{state: SessionBrokerJournalState{Version: SessionBrokerProtocolVersion}}
	first, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	request := validBrokerRequest()
	started := first.Handle(context.Background(), request)
	if started.Code != SessionBrokerOK {
		t.Fatalf("start failed: %+v", started)
	}
	adapter.mu.Lock()
	adapter.active[request.SID] = BootstrapSession{ID: started.SessionID + 1, State: "active"}
	adapter.mu.Unlock()
	second, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	if response := second.Handle(context.Background(), request); response.Code != SessionBrokerSessionChanged {
		t.Fatalf("session reuse was adopted: %+v", response)
	}
	stop := request
	stop.Operation, stop.SessionID = SessionBrokerStop, started.SessionID
	if response := second.Handle(context.Background(), stop); response.Code != SessionBrokerSessionChanged {
		t.Fatalf("session reuse was stopped: %+v", response)
	}
}

func TestFileSessionBrokerJournalIsAtomicAndCredentialFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownership.json")
	journal, err := NewFileSessionBrokerJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	state := SessionBrokerJournalState{Version: SessionBrokerProtocolVersion, Records: []SessionBrokerOwnership{{PoolID: "pool-a", SlotID: "slot-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-1-2-3-4", SessionID: 44, Owner: "owner-1"}}}
	if err := journal.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "secret") {
		t.Fatalf("journal read failed or contained a secret: err=%v data=%s", err, raw)
	}
	loaded, err := journal.Load(context.Background())
	if err != nil || len(loaded.Records) != 1 || loaded.Records[0].SessionID != 44 {
		t.Fatalf("journal load=%+v err=%v", loaded, err)
	}
	if err := os.WriteFile(path, []byte("{\"version\":1,\"records\":[],\"unexpected\":true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Load(context.Background()); !errors.Is(err, ErrSessionBrokerUnknownOwnership) {
		t.Fatalf("unknown journal field err=%v", err)
	}
	if err := os.WriteFile(path, []byte("{\"records\":[]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionBrokerWithJournal(newBrokerTestAdapter(), journal); !errors.Is(err, ErrSessionBrokerUnknownOwnership) {
		t.Fatalf("missing journal version err=%v", err)
	}
}

func TestSessionBrokerStopHasBoundedWait(t *testing.T) {
	adapter := newBrokerTestAdapter()
	adapter.keepAlive = true
	broker := NewSessionBroker(adapter)
	request := validBrokerRequest()
	started := broker.Handle(context.Background(), request)
	stop := request
	stop.Operation, stop.SessionID = SessionBrokerStop, started.SessionID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if response := broker.Handle(ctx, stop); response.Code != SessionBrokerStopTimeout {
		t.Fatalf("stop did not time out: %+v", response)
	}
}

type blockingStartAdapter struct{}

func (blockingStartAdapter) Start(ctx context.Context, _ SessionBrokerOwnership) (BootstrapSession, error) {
	<-ctx.Done()
	return BootstrapSession{}, ctx.Err()
}

func (blockingStartAdapter) Stop(context.Context, SessionBrokerOwnership) error { return nil }

func (blockingStartAdapter) Find(context.Context, string) (BootstrapSession, error) {
	return BootstrapSession{State: "logged_off"}, ErrSessionUnavailable
}

func TestSessionBrokerStartProviderTimeoutIsStable(t *testing.T) {
	broker := NewSessionBroker(blockingStartAdapter{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	response := broker.Handle(ctx, validBrokerRequest())
	if response.Code != SessionBrokerSessionUnavailable || response.SessionID != 0 {
		t.Fatalf("timeout response=%+v", response)
	}
}

type changingSessionAdapter struct {
	finds int
	stops int
	gone  bool
}

func (a *changingSessionAdapter) Start(context.Context, SessionBrokerOwnership) (BootstrapSession, error) {
	return BootstrapSession{ID: 71, State: "active"}, nil
}

func (a *changingSessionAdapter) Stop(context.Context, SessionBrokerOwnership) error {
	a.stops++
	a.gone = true
	return nil
}

func (a *changingSessionAdapter) Find(context.Context, string) (BootstrapSession, error) {
	a.finds++
	if a.gone {
		return BootstrapSession{State: "logged_off"}, ErrSessionUnavailable
	}
	if a.finds == 1 {
		return BootstrapSession{State: "logged_off"}, ErrSessionUnavailable
	}
	return BootstrapSession{ID: 72, State: "active"}, nil
}

func TestSessionBrokerRejectsProviderSessionIDChange(t *testing.T) {
	adapter := &changingSessionAdapter{}
	broker := NewSessionBroker(adapter)
	response := broker.Handle(context.Background(), validBrokerRequest())
	if response.Code != SessionBrokerSessionChanged || response.SessionID != 0 {
		t.Fatalf("changed session response=%+v", response)
	}
	if adapter.stops != 1 {
		t.Fatalf("cleanup stops=%d, want 1", adapter.stops)
	}
}

type failingCleanupAdapter struct {
	*brokerTestAdapter
	queryErr   error
	cleanupErr error
}

func (a *failingCleanupAdapter) Find(ctx context.Context, sid string) (BootstrapSession, error) {
	if a.queryErr != nil {
		return BootstrapSession{}, a.queryErr
	}
	return a.brokerTestAdapter.Find(ctx, sid)
}
func (a *failingCleanupAdapter) Stop(ctx context.Context, o SessionBrokerOwnership) error {
	if err := a.brokerTestAdapter.Stop(ctx, o); err != nil {
		return err
	}
	return a.cleanupErr
}

func TestBrokerRetainsOwnershipWhenCredentialCleanupOrQueryFails(t *testing.T) {
	ctx := context.Background()
	journal, err := NewFileSessionBrokerJournal(filepath.Join(t.TempDir(), "ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &failingCleanupAdapter{brokerTestAdapter: newBrokerTestAdapter()}
	broker, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	request := validBrokerRequest()
	started := broker.Handle(ctx, request)
	if started.Code != SessionBrokerOK {
		t.Fatal(started)
	}
	stop := request
	stop.Operation = SessionBrokerStop
	stop.SessionID = started.SessionID
	for _, stage := range []string{"query", "credential"} {
		if stage == "query" {
			adapter.queryErr = ErrSessionQuery
		} else {
			adapter.queryErr = nil
			adapter.cleanupErr = ErrSessionUnavailable
		}
		if response := broker.Handle(ctx, stop); response.Code == SessionBrokerOK {
			t.Fatalf("%s failure accepted", stage)
		}
		state, err := journal.Load(ctx)
		if err != nil || len(state.Records) != 1 || len(state.Tombstones) != 0 {
			t.Fatalf("%s ownership lost: %+v %v", stage, state, err)
		}
	}
	adapter.cleanupErr = nil
	restarted, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	if response := restarted.Handle(ctx, stop); response.Code != SessionBrokerOK {
		t.Fatal(response)
	}
	state, err := journal.Load(ctx)
	if err != nil || len(state.Records) != 0 || len(state.Tombstones) != 1 {
		t.Fatalf("cleanup not committed %+v %v", state, err)
	}
}

func TestBrokerQueryFailureDoesNotAuthorizeStartOrAbsence(t *testing.T) {
	for _, cause := range []error{ErrSessionQuery, ErrSessionUnavailable} {
		adapter := &failingCleanupAdapter{brokerTestAdapter: newBrokerTestAdapter(), queryErr: cause}
		broker := NewSessionBroker(adapter)
		if response := broker.Handle(context.Background(), validBrokerRequest()); response.Code == SessionBrokerOK || adapter.starts != 0 {
			t.Fatalf("unknown query authorized login %+v", response)
		}
		if err := waitBrokerSessionGone(context.Background(), adapter, validBrokerRequest().SID); err == nil {
			t.Fatal("unknown query authorized absence")
		}
	}
	request := validBrokerRequest()
	request.PoolID = ""
	if _, err := EncodeSessionBrokerRequest(request); err == nil {
		t.Fatal("missing pool accepted")
	}
}

func TestSessionBrokerNewGenerationClearsTombstoneAndRecovers(t *testing.T) {
	ctx := context.Background()
	adapter := newBrokerTestAdapter()
	journal := &memorySessionBrokerJournal{state: SessionBrokerJournalState{Version: SessionBrokerProtocolVersion}}
	first, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	request := validBrokerRequest()
	request.Generation = 1
	started := first.Handle(ctx, request)
	if started.Code != SessionBrokerOK {
		t.Fatalf("generation 1 start failed: %+v", started)
	}
	stop := request
	stop.Operation = SessionBrokerStop
	stop.SessionID = started.SessionID
	if response := first.Handle(ctx, stop); response.Code != SessionBrokerOK {
		t.Fatalf("generation 1 stop failed: %+v", response)
	}
	state, err := journal.Load(ctx)
	if err != nil || len(state.Records) != 0 || len(state.Tombstones) != 1 {
		t.Fatalf("generation 1 tombstone missing: %+v %v", state, err)
	}

	next := request
	next.Generation++
	nextStarted := first.Handle(ctx, next)
	if nextStarted.Code != SessionBrokerOK || nextStarted.SessionID == started.SessionID {
		t.Fatalf("generation 2 start failed or reused session: %+v", nextStarted)
	}
	state, err = journal.Load(ctx)
	if err != nil || len(state.Records) != 1 || len(state.Tombstones) != 0 {
		t.Fatalf("generation 2 did not replace tombstone: %+v %v", state, err)
	}

	recovered, err := NewSessionBrokerWithJournal(adapter, journal)
	if err != nil {
		t.Fatal(err)
	}
	if response := recovered.Handle(ctx, next); response.Code != SessionBrokerOK || response.SessionID != nextStarted.SessionID {
		t.Fatalf("generation 2 did not recover after restart: %+v", response)
	}
	old := request
	if response := recovered.Handle(ctx, old); response.Code != SessionBrokerStaleGeneration {
		t.Fatalf("old generation was accepted after restart: %+v", response)
	}
	old.Operation = SessionBrokerStop
	old.SessionID = started.SessionID
	if response := recovered.Handle(ctx, old); response.Code != SessionBrokerStaleGeneration {
		t.Fatalf("old generation stop was accepted: %+v", response)
	}
}

type failNextBrokerJournal struct {
	*memorySessionBrokerJournal
	failNext bool
}

func (j *failNextBrokerJournal) Save(ctx context.Context, state SessionBrokerJournalState) error {
	if j.failNext {
		j.failNext = false
		return ErrSessionBrokerUnknownOwnership
	}
	return j.memorySessionBrokerJournal.Save(ctx, state)
}

func TestSessionBrokerFailedNewGenerationCommitRetainsRecoveryFence(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanupFails), func(t *testing.T) {
			ctx := context.Background()
			adapter := &failingCleanupAdapter{brokerTestAdapter: newBrokerTestAdapter()}
			journal := &failNextBrokerJournal{memorySessionBrokerJournal: &memorySessionBrokerJournal{state: SessionBrokerJournalState{Version: SessionBrokerProtocolVersion}}}
			broker, err := NewSessionBrokerWithJournal(adapter, journal)
			if err != nil {
				t.Fatal(err)
			}
			request := validBrokerRequest()
			started := broker.Handle(ctx, request)
			if started.Code != SessionBrokerOK {
				t.Fatal(started)
			}
			stop := request
			stop.Operation, stop.SessionID = SessionBrokerStop, started.SessionID
			if response := broker.Handle(ctx, stop); response.Code != SessionBrokerOK {
				t.Fatal(response)
			}
			next := request
			next.Generation++
			journal.failNext = true
			if cleanupFails {
				adapter.cleanupErr = ErrSessionUnavailable
			}
			if response := broker.Handle(ctx, next); response.Code == SessionBrokerOK {
				t.Fatal("failed journal commit reported success")
			}
			state, err := journal.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewSessionBrokerWithJournal(adapter, journal)
			if err != nil {
				t.Fatalf("failed Start stranded journal: %v", err)
			}
			if cleanupFails {
				if len(state.Records) != 1 || len(state.Tombstones) != 0 || state.Records[0].Generation != next.Generation {
					t.Fatalf("failed compensation lost new fence: %+v", state)
				}
				adapter.cleanupErr = nil
				stop = next
				stop.Operation, stop.SessionID = SessionBrokerStop, state.Records[0].SessionID
			} else if len(state.Records) != 0 || len(state.Tombstones) != 1 || state.Tombstones[0].Generation != request.Generation {
				t.Fatalf("successful compensation lost old tombstone: %+v", state)
			}
			if response := restarted.Handle(ctx, stop); response.Code != SessionBrokerOK {
				t.Fatalf("restart cleanup/idempotent stop failed: %+v", response)
			}
		})
	}
}
