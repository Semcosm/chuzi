package slotwindows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func validBrokerRequest() SessionBrokerRequest {
	return SessionBrokerRequest{
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
		{"unknown field", `{"version":1,"operation":"start","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1","password":"secret"}`, SessionBrokerInvalidRequest},
		{"invalid version", `{"version":2,"operation":"start","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidVersion},
		{"unknown operation", `{"version":1,"operation":"restart","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerUnknownOperation},
		{"empty slot", `{"version":1,"operation":"start","slot_id":"","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"invalid sid", `{"version":1,"operation":"start","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"not-a-sid","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"zero generation", `{"version":1,"operation":"start","slot_id":"slot-001","ordinal":1,"generation":0,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"invalid ordinal", `{"version":1,"operation":"start","slot_id":"slot-001","ordinal":257,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
		{"stop without session", `{"version":1,"operation":"stop","slot_id":"slot-001","ordinal":1,"generation":7,"sid":"S-1-5-21-1-2-3-4","owner":"scheduler-1"}`, SessionBrokerInvalidRequest},
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
	if _, err := DecodeSessionBrokerResponse([]byte(`{"version":1,"operation":"start","code":"made_up","session_id":1,"state":"active"}`)); !errors.Is(err, ErrSessionBrokerInvalidRequest) {
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
		return BootstrapSession{}, ErrSessionUnavailable
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
	journal := &memorySessionBrokerJournal{state: SessionBrokerJournalState{Version: 1}}
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
	journal := &memorySessionBrokerJournal{state: SessionBrokerJournalState{Version: 1}}
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
	state := SessionBrokerJournalState{Version: 1, Records: []SessionBrokerOwnership{{SlotID: "slot-001", Ordinal: 1, Generation: 7, SID: "S-1-5-21-1-2-3-4", SessionID: 44, Owner: "owner-1"}}}
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
	return BootstrapSession{}, ErrSessionUnavailable
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
		return BootstrapSession{}, ErrSessionUnavailable
	}
	if a.finds == 1 {
		return BootstrapSession{}, ErrSessionUnavailable
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
