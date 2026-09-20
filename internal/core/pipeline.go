package core

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/automation"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/queue"
	"github.com/Semcosm/chuzi/internal/store"
)

var (
	ErrInvalidPipeline = errors.New("core: invalid pipeline")
)

const genshinCloudGameOperation = "genshin.cloudgame.session_probe"

// CredentialProvider is the least-privilege credential boundary needed by a
// session. credential.Service satisfies it; tests can provide a memory fake.
// The callback must not retain the supplied buffer.
type CredentialProvider interface {
	Use(context.Context, string, string, time.Time, func([]byte) error) error
}

// PipelineConfig describes the dependencies of one queue session. It keeps
// browser, credential and automation implementations behind their existing
// package contracts, so replacing a fake with a process-backed implementation
// does not change scheduler behavior.
type PipelineConfig struct {
	Factory     browser.WorkerFactory
	Credentials CredentialProvider
	Automation  automation.Adapter
	Profiles    *browser.Profiles
	Browser     browser.Config
	Clock       func() time.Time
	Actor       string
	Operation   automation.Operation
	// CredentialOptional allows a session-check operation to run against an
	// already-authenticated Profile without requiring a stored secret. It does
	// not expose credentials to an adapter; credential-aware operations still
	// use the callback boundary.
	CredentialOptional bool
}

// PipelineRunner adapts the existing browser Session Runner into a queue
// Runner. The wrapped worker first establishes its runtime lifecycle, then the
// pipeline performs Credential.Use and Automation.Execute. Only classified
// runtime facts cross back to queue; account state transitions remain owned by
// queue/store/account.
type PipelineRunner struct {
	session *browser.Runner
}

var _ queue.Runner = (*PipelineRunner)(nil)
var _ CredentialProvider = (*credential.Service)(nil)

// NewPipelineRunner builds a queue runner with a durable lease keeper. The
// supplied store is used only by browser.Runner for heartbeats and cancellation
// observation; all business transitions are still performed by queue.
func NewPipelineRunner(database *store.Store, config PipelineConfig) (*PipelineRunner, error) {
	if database == nil || config.Factory == nil || config.Credentials == nil ||
		config.Automation == nil || config.Profiles == nil || config.Clock == nil ||
		strings.TrimSpace(config.Actor) == "" {
		return nil, ErrInvalidPipeline
	}
	if strings.TrimSpace(config.Operation.Name) == "" {
		return nil, ErrInvalidPipeline
	}
	if config.Operation.ID == "" {
		// The operation ID is made request-specific in pipelineWorker.Start.
		config.Operation.ID = "operation"
	}
	config.Browser.Clock = config.Clock
	if config.Browser.LeaseTTL <= 0 {
		return nil, ErrInvalidPipeline
	}
	wrapped := &pipelineFactory{
		delegate:           config.Factory,
		credentials:        config.Credentials,
		automation:         config.Automation,
		clock:              config.Clock,
		actor:              config.Actor,
		operation:          config.Operation,
		credentialOptional: config.CredentialOptional,
	}
	if config.Browser.CancellationObserver == nil {
		config.Browser.CancellationObserver = database
	}
	session, err := browser.New(wrapped, database, config.Profiles, config.Browser)
	if err != nil {
		return nil, err
	}
	return &PipelineRunner{session: session}, nil
}

// Run implements queue.Runner and delegates lifecycle cancellation, timeout,
// lease heartbeat, and profile isolation to browser.Runner.
func (r *PipelineRunner) Run(ctx context.Context, work queue.Work) (queue.Result, error) {
	if r == nil || r.session == nil {
		return queue.Result{}, ErrInvalidPipeline
	}
	return r.session.Run(ctx, work)
}

type pipelineFactory struct {
	delegate           browser.WorkerFactory
	credentials        CredentialProvider
	automation         automation.Adapter
	clock              func() time.Time
	actor              string
	operation          automation.Operation
	credentialOptional bool
}

func (f *pipelineFactory) Start(ctx context.Context, spec browser.WorkerSpec) (browser.Worker, error) {
	worker, err := f.delegate.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, browser.ErrWorkerCrashed
	}
	operation := f.operation
	// Keep request executions isolated even when an adapter mutates its input
	// map. The configured operation is reused for every queued request.
	if operation.Parameters != nil {
		parameters := make(map[string]string, len(operation.Parameters))
		for key, value := range operation.Parameters {
			parameters[key] = value
		}
		operation.Parameters = parameters
	}
	if operation.ID == "" || operation.ID == "operation" {
		operation.ID = "operation-" + spec.RequestID
	}
	if operation.Deadline.IsZero() {
		// The outer queue context owns the hard deadline. Keeping this value zero
		// avoids inventing a second clock or an unbounded wall-clock deadline.
		operation.Deadline = time.Time{}
	}
	runtime := spec.Mode
	if runtime == "adapter" {
		runtime = "headless-cdp"
	}
	session := automation.Session{
		SessionID:  spec.SessionID,
		AccountID:  spec.AccountID,
		RequestID:  spec.RequestID,
		ProfileDir: spec.ProfileDir,
		Runtime:    runtime,
		Handle:     "",
	}
	return &pipelineWorker{
		worker:             worker,
		credentials:        f.credentials,
		automation:         f.automation,
		clock:              f.clock,
		actor:              f.actor,
		session:            session,
		operation:          operation,
		requireHandle:      spec.Mode == "adapter",
		credentialOptional: f.credentialOptional,
	}, nil
}

type pipelineWorker struct {
	worker             browser.Worker
	credentials        CredentialProvider
	automation         automation.Adapter
	clock              func() time.Time
	actor              string
	session            automation.Session
	operation          automation.Operation
	requireHandle      bool
	credentialOptional bool
}

func (w *pipelineWorker) Snapshot(ctx context.Context, width, height int) (browser.ViewSnapshot, error) {
	if w == nil || w.worker == nil {
		return browser.ViewSnapshot{}, browser.ErrViewUnavailable
	}
	viewer, ok := w.automation.(automation.Viewer)
	if !ok {
		return browser.ViewSnapshot{}, browser.ErrViewUnavailable
	}
	frame, err := viewer.Snapshot(ctx, w.session, width, height)
	if err != nil {
		return browser.ViewSnapshot{}, err
	}
	return browser.ViewSnapshot{
		ContentType: frame.ContentType,
		Width:       frame.Width,
		Height:      frame.Height,
		Data:        frame.Data,
	}, nil
}

func (w *pipelineWorker) Run(ctx context.Context) (browser.WorkerResult, error) {
	if ctx == nil || w == nil || w.worker == nil || w.credentials == nil || w.automation == nil || w.clock == nil {
		return browser.WorkerResult{Failure: account.ConfigurationFailure}, nil
	}
	runtimeResult, err := w.worker.Run(ctx)
	if err != nil || !runtimeResult.Succeeded {
		return runtimeResult, err
	}
	// Do not invoke credentials or the adapter after malformed worker output.
	// browser.Runner validates its final result too, but that check happens
	// after this method returns and would otherwise permit a side effect first.
	if runtimeResult.Failure != "" {
		return browser.WorkerResult{Failure: account.UnknownFailure}, nil
	}
	// Adapter mode returns an ephemeral handle for the browser session that
	// already owns this Profile. Carry it only in memory to prevent the adapter
	// from launching a second browser or accepting a caller-selected endpoint.
	if w.requireHandle && runtimeResult.Handle == "" {
		return browser.WorkerResult{Failure: account.UnknownFailure}, nil
	}
	if runtimeResult.Handle != "" {
		if !validRuntimeHandle(runtimeResult.Handle) {
			return browser.WorkerResult{Failure: account.UnknownFailure}, nil
		}
		w.session.Handle = runtimeResult.Handle
	}
	if err := ctx.Err(); err != nil {
		return browser.WorkerResult{Failure: account.TransientFailure}, err
	}
	at := w.clock()
	if at.IsZero() {
		return browser.WorkerResult{Failure: account.ConfigurationFailure}, nil
	}
	if err := w.session.Validate(); err != nil {
		return browser.WorkerResult{Failure: account.ConfigurationFailure}, nil
	}
	if err := w.operation.Validate(); err != nil {
		return browser.WorkerResult{Failure: account.ConfigurationFailure}, nil
	}
	var automationResult automation.Result
	var automationErr error
	var credentialErr error
	var callbackErr error
	credentialUse := automation.CredentialUse(func(useCtx context.Context, fn func([]byte) error) error {
		if useCtx == nil || fn == nil {
			return ErrInvalidPipeline
		}
		// The adapter may provide an earlier cancellation signal, but it must not
		// be able to replace the operation context with an unbounded one. Core
		// owns the credential lifetime and always binds decryption to ctx.
		if err := useCtx.Err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		callbackErr = nil
		credentialErr = w.credentials.Use(ctx, w.session.AccountID, w.actor, at, func(payload []byte) error {
			callbackErr = fn(payload)
			return callbackErr
		})
		if callbackErr != nil {
			// Preserve an adapter error returned by the callback. The provider may
			// return the same error (or wrap it) after the callback completes.
			automationErr = callbackErr
		}
		return credentialErr
	})
	if aware, ok := w.automation.(automation.CredentialAwareAdapter); ok {
		// Credential-aware adapters decide whether the operation needs a secret;
		// Core only binds the least-privilege callback and never receives the
		// plaintext itself.
		var awareErr error
		automationResult, awareErr = aware.ExecuteWithCredential(ctx, w.session, w.operation, credentialUse)
		if awareErr != nil && (callbackErr == nil || !errors.Is(awareErr, callbackErr)) &&
			(credentialErr == nil || !errors.Is(awareErr, credentialErr)) {
			// The returned error is not the provider error or a callback error,
			// so it belongs to the adapter itself.
			automationErr = awareErr
		}
	} else {
		if w.credentialOptional {
			// Session probes operate on an already-authorized browser Profile and
			// therefore do not need a stored credential.
			automationResult, automationErr = w.automation.Execute(ctx, w.session, w.operation)
		} else {
			// Keep the legacy gate for adapters that only implement the transport
			// neutral contract. Such adapters cannot receive credential bytes.
			credentialErr = w.credentials.Use(ctx, w.session.AccountID, w.actor, at, func(_ []byte) error {
				automationResult, automationErr = w.automation.Execute(ctx, w.session, w.operation)
				return automationErr
			})
		}
	}
	if automationErr != nil {
		// Errors returned by the adapter callback/execution must not be mistaken
		// for credential backend failures. Queue can then apply transient retry.
		if errors.Is(automationErr, context.Canceled) || errors.Is(automationErr, context.DeadlineExceeded) {
			return browser.WorkerResult{Failure: account.TransientFailure}, automationErr
		}
		if errors.Is(automationErr, automation.ErrInvalidContract) || errors.Is(automationErr, automation.ErrUnsupported) {
			return browser.WorkerResult{Failure: account.ConfigurationFailure}, nil
		}
		return browser.WorkerResult{Failure: account.TransientFailure}, automationErr
	}
	if credentialErr != nil {
		if errors.Is(credentialErr, context.Canceled) || errors.Is(credentialErr, context.DeadlineExceeded) {
			return browser.WorkerResult{Failure: account.TransientFailure}, credentialErr
		}
		return browser.WorkerResult{Failure: account.CredentialFailure}, nil
	}
	if err := automationResult.Validate(); err != nil {
		return browser.WorkerResult{Failure: account.UnknownFailure}, nil
	}
	if automationResult.Succeeded {
		if failure, ok := evaluateOperation(w.operation, automationResult); !ok {
			return browser.WorkerResult{Failure: failure}, nil
		}
		return browser.WorkerResult{Succeeded: true}, nil
	}
	return browser.WorkerResult{Failure: mapAutomationFailure(automationResult.Failure)}, nil
}

// evaluateOperation is the Core-owned business-result boundary. Adapters
// only complete page operations and return bounded observations; this function
// turns operation-specific observations into account-level outcomes.
func evaluateOperation(operation automation.Operation, result automation.Result) (account.FailureClass, bool) {
	if operation.Name != genshinCloudGameOperation {
		return "", true
	}
	facts := result.Facts
	if facts["platform"] != "genshin-cloudgame" || facts["flow"] != "authorized-session-check" ||
		facts["page"] != "recognized" || facts["shell"] != "present" {
		return account.UnknownFailure, false
	}
	switch facts["session"] {
	case "authenticated":
		return "", true
	case "not_authenticated":
		return account.CredentialFailure, false
	default:
		return account.UnknownFailure, false
	}
}

func validRuntimeHandle(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "headless-cdp" || parsed.Hostname() != "127.0.0.1" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	portText := parsed.Port()
	port, err := strconv.Atoi(portText)
	return portText != "" && err == nil && port >= 1 && port <= 65535
}

func (w *pipelineWorker) Cancel(ctx context.Context) error {
	if ctx == nil || w == nil {
		return ErrInvalidPipeline
	}
	// Cancellation must reach both boundaries. Run calls concurrently so a
	// stuck adapter cannot prevent the browser process from being reaped before
	// the session runner's bounded cancellation deadline.
	type cancelResult struct {
		adapter bool
		err     error
	}
	results := make(chan cancelResult, 2)
	var calls sync.WaitGroup
	calls.Add(2)
	go func() {
		defer calls.Done()
		if w.automation == nil {
			results <- cancelResult{adapter: true, err: ErrInvalidPipeline}
			return
		}
		results <- cancelResult{adapter: true, err: w.automation.Cancel(ctx, w.operation.ID)}
	}()
	go func() {
		defer calls.Done()
		if w.worker == nil {
			results <- cancelResult{err: ErrInvalidPipeline}
			return
		}
		results <- cancelResult{err: w.worker.Cancel(ctx)}
	}()
	calls.Wait()
	close(results)
	var adapterErr, workerErr error
	for result := range results {
		if result.adapter {
			adapterErr = result.err
		} else {
			workerErr = result.err
		}
	}
	if adapterErr != nil {
		return adapterErr
	}
	return workerErr
}

func (w *pipelineWorker) Close(ctx context.Context) error {
	if w == nil || w.worker == nil {
		return ErrInvalidPipeline
	}
	return w.worker.Close(ctx)
}

func mapAutomationFailure(failure *automation.Failure) account.FailureClass {
	if failure == nil {
		return account.UnknownFailure
	}
	switch failure.Class {
	case automation.FailureAuthentication:
		return account.CredentialFailure
	case automation.FailurePermission:
		return account.PermissionFailure
	case automation.FailureConfiguration:
		return account.ConfigurationFailure
	case automation.FailureRuntime, automation.FailureTransient, automation.FailureCancelled:
		return account.TransientFailure
	case automation.FailureBusiness:
		return account.UnknownFailure
	default:
		return account.UnknownFailure
	}
}
