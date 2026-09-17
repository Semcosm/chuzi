// Package coretest contains deterministic substitutes used by cross-module
// Core integration tests. They intentionally implement the production
// boundaries instead of exposing test-only shortcuts into account or store.
package coretest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/automation"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/core"
	"github.com/Semcosm/chuzi/internal/matrix"
)

var ErrFakeWorkerCrashed = errors.New("coretest: worker crashed")

// Clock is a deterministic, thread-safe clock. It is suitable for both the
// scheduler and notifier, which can then be advanced without sleeping.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(now time.Time) *Clock { return &Clock{now: now} }

func (c *Clock) Now() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func (c *Clock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

// IDs produces stable per-kind IDs without consulting wall clock or randomness.
type IDs struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewIDs() *IDs { return &IDs{counts: make(map[string]int)} }

func (g *IDs) Next(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[kind]++
	return kind + "-" + itoa(g.counts[kind])
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}

// CredentialProvider is a fake of the least-privilege callback boundary.
type CredentialProvider struct {
	mu       sync.Mutex
	Payload  []byte
	Calls    []string
	Failures []error
}

var _ core.CredentialProvider = (*CredentialProvider)(nil)

func (p *CredentialProvider) Use(ctx context.Context, accountID, _ string, _ time.Time, fn func([]byte) error) error {
	if p == nil || fn == nil {
		return errors.New("coretest: invalid credential use")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.Calls = append(p.Calls, accountID)
	var failure error
	if len(p.Failures) > 0 {
		failure = p.Failures[0]
		p.Failures = p.Failures[1:]
	}
	payload := append([]byte(nil), p.Payload...)
	p.mu.Unlock()
	if failure != nil {
		return failure
	}
	defer clearBytes(payload)
	return fn(payload)
}

func (p *CredentialProvider) UseCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Calls)
}

// WorkerPlan controls one fake browser worker lifecycle.
type WorkerPlan struct {
	Result    browser.WorkerResult
	Err       error
	Block     bool
	CancelErr error
	CloseErr  error
	Started   chan struct{}
	Release   chan struct{}
}

// BrowserWorkerFactory is a fake WorkerFactory. Specs are retained so tests
// can assert service-derived session and profile values.
type BrowserWorkerFactory struct {
	mu      sync.Mutex
	Plans   []WorkerPlan
	Specs   []browser.WorkerSpec
	Workers []*BrowserWorker
}

var _ browser.WorkerFactory = (*BrowserWorkerFactory)(nil)

func (f *BrowserWorkerFactory) Start(_ context.Context, spec browser.WorkerSpec) (browser.Worker, error) {
	if f == nil {
		return nil, ErrFakeWorkerCrashed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Specs = append(f.Specs, spec)
	plan := WorkerPlan{Result: browser.WorkerResult{Succeeded: true}}
	if len(f.Plans) > 0 {
		plan = f.Plans[0]
		f.Plans = f.Plans[1:]
	}
	worker := &BrowserWorker{Plan: plan, Cancelled: make(chan struct{}), Closed: make(chan struct{})}
	f.Workers = append(f.Workers, worker)
	return worker, nil
}

type BrowserWorker struct {
	Plan       WorkerPlan
	Cancelled  chan struct{}
	Closed     chan struct{}
	mu         sync.Mutex
	started    sync.Once
	cancelOnce sync.Once
	closeOnce  sync.Once
	CancelN    int
	CloseN     int
}

var _ browser.Worker = (*BrowserWorker)(nil)

func (w *BrowserWorker) Run(ctx context.Context) (browser.WorkerResult, error) {
	if w.Plan.Started != nil {
		w.started.Do(func() { close(w.Plan.Started) })
	}
	if !w.Plan.Block {
		return w.Plan.Result, w.Plan.Err
	}
	select {
	case <-ctx.Done():
		return browser.WorkerResult{Failure: account.TransientFailure}, ctx.Err()
	case <-w.Cancelled:
		return browser.WorkerResult{Failure: account.TransientFailure}, context.Canceled
	case <-w.Plan.Release:
		return w.Plan.Result, w.Plan.Err
	}
}

func (w *BrowserWorker) Cancel(_ context.Context) error {
	w.mu.Lock()
	w.CancelN++
	w.mu.Unlock()
	if w.Cancelled != nil {
		w.cancelOnce.Do(func() { close(w.Cancelled) })
	}
	return w.Plan.CancelErr
}

func (w *BrowserWorker) Close(_ context.Context) error {
	w.mu.Lock()
	w.CloseN++
	w.mu.Unlock()
	if w.Closed != nil {
		w.closeOnce.Do(func() { close(w.Closed) })
	}
	return w.Plan.CloseErr
}

// AutomationAdapter is a deterministic fake of automation.Adapter.
type AutomationAdapter struct {
	mu       sync.Mutex
	Plans    []AutomationPlan
	Sessions []automation.Session
	Ops      []automation.Operation
	Cancels  []string
}

type AutomationPlan struct {
	Result  automation.Result
	Err     error
	Block   bool
	Release chan struct{}
}

var _ automation.Adapter = (*AutomationAdapter)(nil)

func (a *AutomationAdapter) Describe(context.Context) (automation.Descriptor, error) {
	return automation.Descriptor{ID: "coretest", Version: "1", API: automation.APIVersion}, nil
}

func (a *AutomationAdapter) Execute(ctx context.Context, session automation.Session, operation automation.Operation) (automation.Result, error) {
	a.mu.Lock()
	a.Sessions = append(a.Sessions, session)
	a.Ops = append(a.Ops, operation)
	plan := AutomationPlan{Result: automation.Result{Succeeded: true}}
	if len(a.Plans) > 0 {
		plan = a.Plans[0]
		a.Plans = a.Plans[1:]
	}
	a.mu.Unlock()
	if plan.Block {
		select {
		case <-ctx.Done():
			return automation.Result{}, ctx.Err()
		case <-plan.Release:
		}
	}
	return plan.Result, plan.Err
}

func (a *AutomationAdapter) Cancel(_ context.Context, operationID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Cancels = append(a.Cancels, operationID)
	return nil
}
func (a *AutomationAdapter) Close(context.Context) error { return nil }

// MatrixSender records stable transaction IDs and bodies. Failures are
// consumed in order, allowing deterministic retry and duplicate-delivery tests.
type MatrixSender struct {
	mu       sync.Mutex
	Sends    []MatrixSend
	Failures []error
}

type MatrixSend struct {
	RoomID  string
	EventID string
	Body    string
}

var _ matrix.Sender = (*MatrixSender)(nil)

func (s *MatrixSender) Send(ctx context.Context, roomID, eventID, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sends = append(s.Sends, MatrixSend{RoomID: roomID, EventID: eventID, Body: body})
	if len(s.Failures) == 0 {
		return nil
	}
	err := s.Failures[0]
	s.Failures = s.Failures[1:]
	return err
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
