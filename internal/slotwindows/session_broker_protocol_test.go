package slotwindows

import (
	"context"
	"errors"
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
}

func (a *changingSessionAdapter) Start(context.Context, SessionBrokerOwnership) (BootstrapSession, error) {
	return BootstrapSession{ID: 71, State: "active"}, nil
}

func (a *changingSessionAdapter) Stop(context.Context, SessionBrokerOwnership) error {
	a.stops++
	return nil
}

func (a *changingSessionAdapter) Find(context.Context, string) (BootstrapSession, error) {
	a.finds++
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
