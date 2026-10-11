package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type runtimeFakes struct {
	host     *fakeHost
	profiles *fakeProfiles
	accounts *fakeAccounts
	login    *fakeLogin
	sessions *fakeSessions
	desktop  *fakeDesktop
	agent    *fakeAgent
	worker   *fakeWorker
	journal  *fakeJournal
}

func newRuntimeFakes(now time.Time) runtimeFakes {
	identity := validIdentity()
	session := Session{ID: 7, SID: identity.SID, State: "active", ObservedAt: now}
	sessions := &fakeSessions{active: session}
	return runtimeFakes{
		host:     &fakeHost{facts: HostFacts{Edition: "Windows 11 Pro", BuildClass: "23H2", Capacity: 4, RDPEnabled: true, NLARequired: true, CertTrusted: true, ObservedAt: now}},
		profiles: &fakeProfiles{}, accounts: &fakeAccounts{}, login: &fakeLogin{session: session}, sessions: sessions,
		desktop: &fakeDesktop{}, agent: &fakeAgent{}, worker: &fakeWorker{}, journal: &fakeJournal{},
	}
}

func (f runtimeFakes) deps() RuntimeDependencies {
	return RuntimeDependencies{Host: f.host, Profiles: f.profiles, Accounts: f.accounts, Login: f.login, Sessions: f.sessions, Desktop: f.desktop, Agent: f.agent, Worker: f.worker, Journal: f.journal}
}

type fakeHost struct {
	facts HostFacts
	err   error
	check func(context.Context) error
}

func (f *fakeHost) Check(ctx context.Context) (HostFacts, error) {
	if f.check != nil {
		if err := f.check(ctx); err != nil {
			return HostFacts{}, err
		}
	}
	return f.facts, f.err
}

type fakeProfiles struct {
	ensure, remove int
	err            error
}

func (f *fakeProfiles) Ensure(context.Context, Identity, ProfileSpec) error { f.ensure++; return f.err }
func (f *fakeProfiles) Remove(context.Context, Identity) error              { f.remove++; return f.err }

type fakeAccounts struct {
	lease                   SecretLease
	create, recover, delete int
	err                     error
}

func (f *fakeAccounts) Create(context.Context, Identity) (SecretLease, error) {
	f.create++
	return f.lease, f.err
}
func (f *fakeAccounts) Recover(context.Context, Identity) (SecretLease, error) {
	f.recover++
	return f.lease, f.err
}
func (f *fakeAccounts) Delete(context.Context, Identity) error { f.delete++; return f.err }

type fakeLogin struct {
	session        Session
	login, logout  int
	err, logoutErr error
	afterLogin     func()
}

func (f *fakeLogin) Login(context.Context, Identity, SecretLease) (Session, error) {
	f.login++
	if f.afterLogin != nil {
		f.afterLogin()
	}
	return f.session, f.err
}
func (f *fakeLogin) Logout(context.Context, Identity, Session) error { f.logout++; return f.logoutErr }

type fakeSessions struct {
	mu               sync.Mutex
	active           Session
	finds, stops     int
	findErr, stopErr error
}

func (f *fakeSessions) Start(context.Context, Identity) (Session, error) { return f.active, nil }
func (f *fakeSessions) Stop(_ context.Context, _ Identity, session Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.stopErr != nil {
		return f.stopErr
	}
	if f.active.ID == session.ID {
		f.active = Session{State: "logged_off"}
	}
	return nil
}
func (f *fakeSessions) Find(context.Context, string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds++
	if f.findErr != nil {
		return Session{}, f.findErr
	}
	if f.active.ID == 0 {
		return Session{State: "logged_off"}, ErrSessionUnavailable
	}
	return f.active, nil
}

type fakeDesktop struct {
	ensure, stop int
	err, stopErr error
}

func (f *fakeDesktop) Ensure(_ context.Context, _ Identity, session Session) (Desktop, error) {
	f.ensure++
	if f.err != nil {
		return Desktop{}, f.err
	}
	return Desktop{SessionID: session.ID, Generation: validIdentity().Generation, ReadyAt: session.ObservedAt}, nil
}
func (f *fakeDesktop) Stop(context.Context, Identity, Desktop) error { f.stop++; return f.stopErr }

type fakeAgent struct {
	start, stop, health     int
	healthValue             AgentHealth
	err, stopErr, healthErr error
}

func (f *fakeAgent) Start(context.Context, Identity, Session, Desktop) error { f.start++; return f.err }
func (f *fakeAgent) Stop(context.Context, Identity) error                    { f.stop++; return f.stopErr }
func (f *fakeAgent) Health(context.Context, Identity) (AgentHealth, error) {
	f.health++
	if f.healthValue.ObservedAt.IsZero() {
		f.healthValue = AgentHealth{Ready: true, Generation: validIdentity().Generation, ObservedAt: time.Unix(100, 0).UTC()}
	}
	return f.healthValue, f.healthErr
}

type fakeWorker struct {
	start, stop, health     int
	healthValue             WorkerHealth
	err, stopErr, healthErr error
}

func (f *fakeWorker) Start(context.Context, Identity) error { f.start++; return f.err }
func (f *fakeWorker) Stop(context.Context, Identity) error  { f.stop++; return f.stopErr }
func (f *fakeWorker) Health(context.Context, Identity) (WorkerHealth, error) {
	f.health++
	if f.healthValue.ObservedAt.IsZero() {
		f.healthValue = WorkerHealth{Ready: true, Capability: "browser", ObservedAt: time.Unix(100, 0).UTC()}
	}
	return f.healthValue, f.healthErr
}

type fakeJournal struct {
	mu                                 sync.Mutex
	record                             OwnershipRecord
	begin, commit, checkpoint, stopped int
	err                                error
}

func (f *fakeJournal) Begin(_ context.Context, r OwnershipRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begin++
	if f.err != nil {
		return f.err
	}
	f.record = r
	return nil
}
func (f *fakeJournal) Commit(_ context.Context, r OwnershipRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commit++
	if f.err != nil {
		return f.err
	}
	f.record = r
	return nil
}
func (f *fakeJournal) Checkpoint(_ context.Context, r OwnershipRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoint++
	if f.err != nil {
		return f.err
	}
	f.record = r
	return nil
}
func (f *fakeJournal) Load(context.Context, string) (OwnershipRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.record.Version == 0 {
		return OwnershipRecord{}, ErrJournalNotFound
	}
	return f.record, nil
}
func (f *fakeJournal) MarkStopped(_ context.Context, r OwnershipRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
	if f.err != nil {
		return f.err
	}
	f.record = r
	return nil
}

func TestRuntimeStartStopRevalidatesSessionAndWipesSecret(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'s', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	fakes.accounts.lease = lease
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	identity := validIdentity()
	result, err := runtime.Start(context.Background(), identity, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Status.AgentReady || !result.Status.WorkerReady || result.Status.Session.ID != 7 {
		t.Fatalf("unexpected ready result: %#v", result.Status)
	}
	if fakes.journal.begin != 1 || fakes.journal.commit != 1 || fakes.journal.checkpoint != 2 {
		t.Fatalf("journal sequence begin=%d commit=%d checkpoint=%d", fakes.journal.begin, fakes.journal.commit, fakes.journal.checkpoint)
	}
	if err := fakes.accounts.lease.WithPassword(func(password []uint16) error {
		for _, unit := range password {
			if unit != 0 {
				t.Fatalf("password buffer was not zeroized: %v", password)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if fakes.login.login != 1 {
		t.Fatalf("login calls=%d", fakes.login.login)
	}
	stopped, err := runtime.Stop(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status.Phase != PhaseStopped || fakes.sessions.stops != 1 || fakes.journal.stopped != 1 {
		t.Fatalf("stop did not fence owned session: %#v stops=%d journal=%d", stopped.Status, fakes.sessions.stops, fakes.journal.stopped)
	}
}

func TestRuntimeRejectsHostPolicyBeforeOwnershipIntent(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	fakes.host.facts.CertTrusted = false
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), validIdentity(), StartOptions{})
	if CodeOf(err) != CodeUnsupported {
		t.Fatalf("host rejection code=%v err=%v", CodeOf(err), err)
	}
	if fakes.journal.begin != 0 || fakes.accounts.create != 0 {
		t.Fatalf("host rejection performed side effects: begin=%d create=%d", fakes.journal.begin, fakes.accounts.create)
	}
}

func TestRuntimeNeverStopsReusedSessionID(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	fakes.login.session = Session{ID: 7, SID: validIdentity().SID, State: "active", ObservedAt: now}
	fakes.sessions.active = Session{ID: 8, SID: validIdentity().SID, State: "active", ObservedAt: now}
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'s', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	fakes.accounts.lease = lease
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{Now: func() time.Time { return now }, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), validIdentity(), StartOptions{})
	if CodeOf(err) != CodeCleanup {
		t.Fatalf("session reuse code=%v err=%v", CodeOf(err), err)
	}
	if fakes.sessions.stops != 0 {
		t.Fatalf("reused session was stopped: %d", fakes.sessions.stops)
	}
	if fakes.journal.record.Phase != JournalUnknown {
		t.Fatalf("cleanup failure was not quarantined: %s", fakes.journal.record.Phase)
	}
}

func TestRuntimeRecoveryUsesAccountRecover(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'r', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	fakes.accounts.lease = lease
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background(), validIdentity(), StartOptions{Recover: true}); err != nil {
		t.Fatal(err)
	}
	if fakes.accounts.recover != 1 || fakes.accounts.create != 0 {
		t.Fatalf("recovery account calls recover=%d create=%d", fakes.accounts.recover, fakes.accounts.create)
	}
}

func TestRuntimeCancelsAfterLoginAndCleansOwnedSession(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	ctx, cancel := context.WithCancel(context.Background())
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'s', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	fakes.accounts.lease = lease
	fakes.login.afterLogin = cancel
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(ctx, validIdentity(), StartOptions{})
	if CodeOf(err) != CodeCancelled {
		t.Fatalf("cancel code=%v err=%v", CodeOf(err), err)
	}
	if fakes.sessions.stops != 1 || fakes.journal.stopped != 1 {
		t.Fatalf("cancelled login did not clean owned session: stops=%d journal=%d", fakes.sessions.stops, fakes.journal.stopped)
	}
}

func TestRuntimeStopFailureLeavesUnknownOwnership(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'s', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	fakes.accounts.lease = lease
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background(), validIdentity(), StartOptions{}); err != nil {
		t.Fatal(err)
	}
	fakes.sessions.stopErr = errors.New("provider detail must stay private")
	_, err = runtime.Stop(context.Background(), validIdentity())
	if CodeOf(err) != CodeCleanup {
		t.Fatalf("stop failure code=%v err=%v", CodeOf(err), err)
	}
	if fakes.journal.record.Phase != JournalUnknown {
		t.Fatalf("stop failure phase=%s", fakes.journal.record.Phase)
	}
}

type ambiguousGoneSessions struct {
	active Session
	stops  int
}

func (s *ambiguousGoneSessions) Start(context.Context, Identity) (Session, error) {
	return s.active, nil
}
func (s *ambiguousGoneSessions) Stop(context.Context, Identity, Session) error {
	s.stops++
	s.active = Session{}
	return nil
}
func (s *ambiguousGoneSessions) Find(context.Context, string) (Session, error) {
	return s.active, nil
}

func TestRuntimeDoesNotMarkUnknownWTSStateStopped(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'s', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	fakes.accounts.lease = lease
	ambiguous := &ambiguousGoneSessions{active: fakes.sessions.active}
	fakes.sessions = nil
	deps := fakes.deps()
	deps.Sessions = ambiguous
	runtime, err := NewRuntime(deps, RuntimeOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background(), validIdentity(), StartOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Stop(context.Background(), validIdentity())
	if CodeOf(err) != CodeCleanup {
		t.Fatalf("ambiguous WTS state code=%v err=%v", CodeOf(err), err)
	}
	if deps.Journal.(*fakeJournal).record.Phase != JournalUnknown {
		t.Fatalf("ambiguous WTS state phase=%s", deps.Journal.(*fakeJournal).record.Phase)
	}
}

func TestRuntimeBoundsOperationWhenCallerOmitsDeadline(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	fakes := newRuntimeFakes(now)
	fakes.host.check = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	runtime, err := NewRuntime(fakes.deps(), RuntimeOptions{
		Now:              func() time.Time { return now },
		OperationTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), validIdentity(), StartOptions{})
	if CodeOf(err) != CodeDeadline {
		t.Fatalf("operation timeout code=%v err=%v", CodeOf(err), err)
	}
	if fakes.journal.begin != 0 {
		t.Fatal("timed out host check wrote ownership intent")
	}
}
