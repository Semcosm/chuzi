package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/automation"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/core"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/coretransport"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/health"
	"github.com/Semcosm/chuzi/internal/matrix"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/plugin"
	"github.com/Semcosm/chuzi/internal/queue"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var version = "dev"

const (
	backendNode     = "node"
	backendHeadless = "headless"
)

var (
	errInvalidBackend = errors.New("service: invalid browser backend")
	errInvalidOptions = errors.New("service: invalid runtime options")
	serviceSequence   atomic.Uint64
)

type serviceOptions struct {
	configPath             string
	backend                string
	workerCommand          string
	workerScript           string
	headlessBrowserCommand string
	automationAdapter      string
	healthListen           string
	metricsListen          string
	logPath                string
	logMaxBytes            int64
	logMaxFiles            int
	owner                  string
	pollInterval           time.Duration
	leaseTTL               time.Duration
	runTimeout             time.Duration
	heartbeat              time.Duration
	cancelTimeout          time.Duration
	shutdownTimeout        time.Duration
	maxConcurrency         int
	maxAttempts            int
	retryBaseDelay         time.Duration
	retryMaxDelay          time.Duration
}

type serviceRuntime struct {
	store         *store.Store
	requests      *requestservice.Service
	credentials   *credential.Service
	runner        queue.Runner
	automation    automation.Adapter
	scheduler     *queue.Scheduler
	notifier      *matrix.Notifier
	gateway       *matrix.Gateway
	matrixClient  *matrix.HTTPClient
	health        *health.Checker
	healthListen  string
	metrics       *observability.Metrics
	logger        *observability.JSONLogger
	metricsListen string
	coreAPI       coreapi.API
	coreServer    *coretransport.Server
}

func (r *serviceRuntime) refreshMetrics(at time.Time) {
	if r == nil || r.metrics == nil || r.store == nil || at.IsZero() {
		return
	}
	snapshot, err := r.store.OperationalSnapshot(at)
	if err != nil {
		r.metrics.Inc("chuzi_operational_errors_total", observability.Label{Name: "operation", Value: "snapshot"})
		return
	}
	r.metrics.Set("chuzi_database_bytes", float64(snapshot.DatabaseBytes))
	r.metrics.Set("chuzi_accounts", float64(snapshot.Accounts))
	r.metrics.Set("chuzi_requests", float64(snapshot.Requests))
	r.metrics.Set("chuzi_requests_queued", float64(snapshot.QueuedRequests))
	r.metrics.Set("chuzi_requests_delayed", float64(snapshot.DelayedRequests))
	r.metrics.Set("chuzi_requests_deadline", float64(snapshot.DeadlineRequests))
	r.metrics.Set("chuzi_requests_running", float64(snapshot.RunningRequests))
	r.metrics.Set("chuzi_leases_active", float64(snapshot.ActiveLeases))
	r.metrics.Set("chuzi_leases_expired", float64(snapshot.ExpiredLeases))
	r.metrics.Set("chuzi_notifications_pending", float64(snapshot.PendingNotifications))
	r.metrics.Set("chuzi_notifications_claimed", float64(snapshot.ClaimedNotifications))
	r.metrics.Set("chuzi_notifications_expired_claims", float64(snapshot.ExpiredNotificationClaims))
	r.metrics.Set("chuzi_notifications_delivered", float64(snapshot.DeliveredNotifications))
}

func defaultServiceOptions() serviceOptions {
	return serviceOptions{
		configPath:             "configs/example.json",
		backend:                backendNode,
		workerCommand:          "node",
		workerScript:           "browser-worker/src/worker.mjs",
		headlessBrowserCommand: "chromium",
		owner:                  "service",
		pollInterval:           500 * time.Millisecond,
		leaseTTL:               2 * time.Minute,
		runTimeout:             5 * time.Minute,
		heartbeat:              30 * time.Second,
		logMaxBytes:            0,
		logMaxFiles:            0,
		cancelTimeout:          5 * time.Second,
		shutdownTimeout:        5 * time.Second,
		maxConcurrency:         1,
		maxAttempts:            3,
		retryBaseDelay:         time.Second,
		retryMaxDelay:          time.Minute,
	}
}

func newID(kind string) string {
	return fmt.Sprintf("%s-%d-%d", kind, time.Now().UnixNano(), serviceSequence.Add(1))
}

// workerStderr keeps browser/worker diagnostics out of service logs by
// default. CI and local debugging may opt in explicitly; protocol stdout is
// never used for diagnostics.
func workerStderr() io.Writer {
	if os.Getenv("CHUZI_WORKER_DEBUG") == "1" {
		return os.Stderr
	}
	return io.Discard
}

func newWorkerFactory(options serviceOptions) (browser.WorkerFactory, error) {
	switch options.backend {
	case backendNode:
		return browser.NewProcessFactory(browser.ProcessConfig{
			Command: options.workerCommand,
			Script:  options.workerScript,
			Stderr:  workerStderr(),
		})
	case backendHeadless:
		if strings.TrimSpace(options.headlessBrowserCommand) == "" {
			return nil, fmt.Errorf("%w: empty headless browser command", errInvalidOptions)
		}
		workerMode := ""
		if strings.TrimSpace(options.automationAdapter) != "" {
			workerMode = "adapter"
		}
		return browser.NewProcessFactory(browser.ProcessConfig{
			Command:    options.workerCommand,
			Script:     "browser-worker/src/headless.mjs",
			ScriptArgs: []string{"--browser-command", options.headlessBrowserCommand},
			Stderr:     workerStderr(),
			WorkerMode: workerMode,
		})
	default:
		return nil, fmt.Errorf("%w: %q (want %s or %s)", errInvalidBackend, options.backend, backendNode, backendHeadless)
	}
}

func hasCapability(descriptor automation.Descriptor, id, version string) bool {
	for _, capability := range descriptor.Capabilities {
		if capability.ID == id && capability.Version == version {
			return true
		}
	}
	return false
}

func (o serviceOptions) validate() error {
	if strings.TrimSpace(o.owner) == "" || o.pollInterval <= 0 || o.leaseTTL <= 0 ||
		o.runTimeout <= 0 || o.cancelTimeout <= 0 || o.shutdownTimeout <= 0 ||
		o.maxConcurrency < 1 || o.maxAttempts < 1 || o.retryBaseDelay < 0 ||
		o.retryMaxDelay < o.retryBaseDelay || o.heartbeat < 0 ||
		(o.heartbeat > 0 && o.heartbeat >= o.leaseTTL) {
		return fmt.Errorf("%w: invalid service timing or concurrency settings", errInvalidOptions)
	}
	if o.backend != backendNode && o.backend != backendHeadless {
		return fmt.Errorf("%w: %q (want %s or %s)", errInvalidBackend, o.backend, backendNode, backendHeadless)
	}
	if o.backend == backendHeadless && strings.TrimSpace(o.headlessBrowserCommand) == "" {
		return fmt.Errorf("%w: empty headless browser command", errInvalidOptions)
	}
	if strings.TrimSpace(o.automationAdapter) != "" &&
		(strings.TrimSpace(o.automationAdapter) != "genshin-cloudgame" || o.backend != backendHeadless) {
		return fmt.Errorf("%w: genshin-cloudgame requires the headless backend", errInvalidOptions)
	}
	if strings.TrimSpace(o.metricsListen) != "" {
		if _, _, err := net.SplitHostPort(strings.TrimSpace(o.metricsListen)); err != nil {
			return fmt.Errorf("%w: metrics listen must be host:port", errInvalidOptions)
		}
	}
	if o.logMaxBytes < 0 || o.logMaxFiles < 0 {
		return fmt.Errorf("%w: log rotation limits must not be negative", errInvalidOptions)
	}
	return nil
}

func assembleRuntime(cfg config.Config, options serviceOptions, now func() time.Time) (*serviceRuntime, error) {
	factory, err := newWorkerFactory(options)
	if err != nil {
		return nil, err
	}
	return assembleRuntimeWithFactory(cfg, options, now, factory)
}

func assembleRuntimeWithFactory(cfg config.Config, options serviceOptions, now func() time.Time, factory browser.WorkerFactory) (*serviceRuntime, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if factory == nil || now == nil {
		return nil, fmt.Errorf("%w: missing runtime dependency", errInvalidOptions)
	}
	database, err := store.Open(cfg)
	if err != nil {
		return nil, err
	}
	var logger *observability.JSONLogger
	var automationAdapter automation.Adapter
	closeOnError := func(closeErr error) (*serviceRuntime, error) {
		if automationAdapter != nil {
			_ = automationAdapter.Close(context.Background())
		}
		if logger != nil {
			_ = logger.Close()
		}
		_ = database.Close()
		return nil, closeErr
	}
	metrics := observability.NewMetrics()
	for _, definition := range []observability.MetricDefinition{
		{Name: "chuzi_events_total", Help: "Classified chuzi operational events", Kind: observability.Counter},
		{Name: "chuzi_event_duration_seconds", Help: "Duration of classified chuzi operations", Kind: observability.Histogram},
		{Name: "chuzi_operational_errors_total", Help: "Operational snapshot failures", Kind: observability.Counter},
		{Name: "chuzi_database_bytes", Help: "Active database file size", Kind: observability.Gauge},
		{Name: "chuzi_accounts", Help: "Accounts in the active store", Kind: observability.Gauge},
		{Name: "chuzi_requests", Help: "Requests in the active store", Kind: observability.Gauge},
		{Name: "chuzi_requests_queued", Help: "Queued requests", Kind: observability.Gauge},
		{Name: "chuzi_requests_delayed", Help: "Delayed queued requests", Kind: observability.Gauge},
		{Name: "chuzi_requests_deadline", Help: "Queued requests past deadline", Kind: observability.Gauge},
		{Name: "chuzi_requests_running", Help: "Running requests", Kind: observability.Gauge},
		{Name: "chuzi_leases_active", Help: "Active account leases", Kind: observability.Gauge},
		{Name: "chuzi_leases_expired", Help: "Expired account leases", Kind: observability.Gauge},
		{Name: "chuzi_notifications_pending", Help: "Pending Matrix notifications", Kind: observability.Gauge},
		{Name: "chuzi_notifications_claimed", Help: "Claimed Matrix notifications", Kind: observability.Gauge},
		{Name: "chuzi_notifications_expired_claims", Help: "Expired Matrix notification claims", Kind: observability.Gauge},
		{Name: "chuzi_notifications_delivered", Help: "Delivered Matrix notifications", Kind: observability.Gauge},
	} {
		if err := metrics.Register(definition); err != nil {
			return closeOnError(err)
		}
	}
	logPath := options.logPath
	if logPath == "" {
		logPath = cfg.Observability.LogPath
	}
	logMaxBytes := options.logMaxBytes
	if logMaxBytes == 0 {
		logMaxBytes = cfg.Observability.LogMaxBytes
	}
	logMaxFiles := options.logMaxFiles
	if logMaxFiles == 0 {
		logMaxFiles = cfg.Observability.LogMaxFiles
	}
	loggerConfig := observability.LoggerConfig{Writer: os.Stderr, MaxBytes: logMaxBytes, MaxFiles: logMaxFiles}
	if strings.TrimSpace(logPath) != "" {
		loggerConfig.Writer = nil
		loggerConfig.Path = logPath
	}
	var loggerErr error
	logger, loggerErr = observability.NewJSONLogger(loggerConfig)
	if loggerErr != nil {
		return closeOnError(loggerErr)
	}
	sink := observability.MultiSink{metrics, logger}

	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		return closeOnError(err)
	}
	requestService, err := requestservice.New(database, now, requestservice.IDGenerator(newID), options.owner)
	if err != nil {
		return closeOnError(err)
	}
	keyring := credential.NewEnvKeyringWithHistory(cfg.Credentials.KeyEnv, cfg.Credentials.KeyIDEnv, cfg.Credentials.HistoryEnv)
	credentials, err := credential.New(database, keyring)
	if err != nil {
		return closeOnError(err)
	}
	var sessionRunner queue.Runner
	if strings.TrimSpace(options.automationAdapter) != "" {
		node, lookErr := exec.LookPath(options.workerCommand)
		if lookErr != nil {
			return closeOnError(fmt.Errorf("service: automation adapter runtime unavailable"))
		}
		adapterPath := filepath.Join("browser-worker", "src", "headless-adapter.mjs")
		client, startErr := plugin.StartAdapter(context.Background(), plugin.Command{
			Mode: plugin.Native, Executable: node,
			Args:   []string{adapterPath, "--browser-command", options.headlessBrowserCommand},
			Stderr: workerStderr(),
		})
		if startErr != nil {
			return closeOnError(fmt.Errorf("service: automation adapter unavailable"))
		}
		automationAdapter = client
		descriptor, describeErr := client.Describe(context.Background())
		if describeErr != nil || !hasCapability(descriptor, "genshin-cloudgame", "1") {
			return closeOnError(fmt.Errorf("service: requested automation capability unavailable"))
		}
		pipelineRunner, pipelineErr := core.NewPipelineRunner(database, core.PipelineConfig{
			Factory: factory, Credentials: credentials, Automation: client, Profiles: profiles,
			Browser: browser.Config{LeaseTTL: options.leaseTTL, HeartbeatInterval: options.heartbeat, CancelTimeout: options.cancelTimeout, ShutdownTimeout: options.shutdownTimeout, WorkerMode: "adapter", Clock: now, Sink: sink},
			Clock:   now, Actor: options.owner,
			Operation:          automation.Operation{Name: "genshin.cloudgame.session_probe"},
			CredentialOptional: true,
		})
		if pipelineErr != nil {
			return closeOnError(pipelineErr)
		}
		sessionRunner = pipelineRunner
	} else {
		sessionRunner, err = browser.New(factory, database, profiles, browser.Config{
			LeaseTTL:          options.leaseTTL,
			HeartbeatInterval: options.heartbeat,
			CancelTimeout:     options.cancelTimeout,
			ShutdownTimeout:   options.shutdownTimeout,
			Clock:             now,
			Sink:              sink,
		})
	}
	if err != nil {
		return closeOnError(err)
	}
	scheduler, err := queue.New(database, sessionRunner, queue.Config{
		Owner:                options.owner,
		LeaseTTL:             options.leaseTTL,
		RunTimeout:           options.runTimeout,
		MaxGlobalConcurrency: options.maxConcurrency,
		RetryPolicy: account.RetryPolicy{
			MaxAttempts: options.maxAttempts,
			BaseDelay:   options.retryBaseDelay,
			MaxDelay:    options.retryMaxDelay,
		},
		Clock: now,
		NewID: newID,
		Sink:  sink,
	})
	if err != nil {
		return closeOnError(err)
	}
	coreAPI, coreErr := core.New(core.Dependencies{Requests: requestService, Store: database})
	if coreErr != nil {
		return closeOnError(coreErr)
	}
	runtime := &serviceRuntime{
		store: database, requests: requestService, credentials: credentials,
		runner: sessionRunner, automation: automationAdapter, scheduler: scheduler, healthListen: cfg.Health.Listen, metrics: metrics, logger: logger, coreAPI: coreAPI,
	}
	runtime.metricsListen = cfg.Observability.MetricsListen
	if strings.TrimSpace(options.metricsListen) != "" {
		runtime.metricsListen = options.metricsListen
	}
	if cfg.Matrix.HomeserverURL != "" {
		token, ok := os.LookupEnv(cfg.Matrix.AccessTokenEnv)
		if !ok || strings.TrimSpace(token) == "" {
			return closeOnError(fmt.Errorf("service: Matrix access token environment variable is unavailable"))
		}
		client, clientErr := matrix.NewHTTPClient(matrix.HTTPClientConfig{
			HomeserverURL: cfg.Matrix.HomeserverURL,
			AccessToken:   token,
			UserAgent:     "chuzi-service/" + version,
		})
		if clientErr != nil {
			return closeOnError(clientErr)
		}
		notifier, notifierErr := matrix.NewNotifier(database, client, matrix.NotifierConfig{
			Owner: "matrix-notifier", ClaimTTL: time.Minute, RetryBase: time.Second,
			RetryMax: time.Minute, BatchSize: 32, Clock: now, Sink: sink,
		})
		if notifierErr != nil {
			return closeOnError(notifierErr)
		}
		runtime.notifier, runtime.matrixClient = notifier, client
		if cfg.Matrix.SyncEnabled {
			policy := matrix.Policy{Rooms: make(map[string]map[string]matrix.Role, len(cfg.Matrix.Rooms))}
			for room, users := range cfg.Matrix.Rooms {
				policy.Rooms[room] = make(map[string]matrix.Role, len(users))
				for user, role := range users {
					policy.Rooms[room][user] = matrix.Role(role)
				}
			}
			adapter, adapterErr := matrix.NewAdapter(requestService, policy, matrix.Config{UserID: cfg.Matrix.UserID})
			if adapterErr != nil {
				return closeOnError(adapterErr)
			}
			syncTimeout := time.Duration(cfg.Matrix.SyncTimeoutSeconds) * time.Second
			if syncTimeout == 0 {
				syncTimeout = 30 * time.Second
			}
			pollInterval := time.Duration(cfg.Matrix.PollIntervalMillis) * time.Millisecond
			gateway, gatewayErr := matrix.NewGateway(matrix.GatewayConfig{Client: client, Adapter: adapter, SyncTimeout: syncTimeout, PollInterval: pollInterval})
			if gatewayErr != nil {
				return closeOnError(gatewayErr)
			}
			runtime.gateway = gateway
		}
	}
	probes := map[string]health.Probe{
		"service": health.StaticProbe(nil),
		"storage": health.StoreProbe(database),
		"queue": func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			_, err := database.ListQueuedRequests(now())
			return err
		},
		"worker": health.StaticProbe(nil),
	}
	if runtime.matrixClient != nil {
		probes["matrix"] = func(ctx context.Context) error {
			return runtime.matrixClient.Health(ctx)
		}
	}
	checker, checkerErr := health.NewCheckerWithConfig(probes, health.Config{ProbeTimeout: 5 * time.Second, Sink: sink, Clock: now})
	if checkerErr != nil {
		return closeOnError(checkerErr)
	}
	runtime.health = checker
	return runtime, nil
}

func runSelfTest(ctx context.Context, command, script string) error {
	root, err := os.MkdirTemp("", "chuzi-self-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	cfg, err := config.New(filepath.Join(root, "runtime"))
	if err != nil {
		return err
	}
	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		return err
	}
	profile, err := profiles.Prepare("self-test-account")
	if err != nil {
		return err
	}
	factory, err := browser.NewProcessFactory(browser.ProcessConfig{
		Command:    command,
		Script:     script,
		Stderr:     workerStderr(),
		WorkerMode: "success",
	})
	if err != nil {
		return err
	}
	worker, err := factory.Start(ctx, browser.WorkerSpec{
		SessionID:  "self-test-session",
		AccountID:  "self-test-account",
		RequestID:  "self-test-request",
		ProfileDir: profile,
		LeaseID:    "self-test-lease",
		Owner:      "self-test",
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = worker.Close(closeCtx)
	}()
	result, err := worker.Run(ctx)
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return fmt.Errorf("worker self-test failed: %s", result.Failure)
	}
	return nil
}

func run(ctx context.Context, options serviceOptions) error {
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(options.healthListen) != "" {
		cfg.Health.Listen = options.healthListen
	}
	runtime, err := assembleRuntime(cfg, options, time.Now)
	if err != nil {
		return err
	}
	defer func() {
		if runtime.coreServer != nil {
			_ = runtime.coreServer.Close()
		}
		if runtime.automation != nil {
			_ = runtime.automation.Close(context.Background())
		}
		if closeErr := runtime.store.Close(); closeErr != nil {
			if runtime.logger != nil {
				runtime.logger.Record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "failed", ErrorClass: "store_close_failed"})
			}
		}
		if runtime.logger != nil {
			runtime.logger.Record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "stopping"})
			_ = runtime.logger.Close()
		}
	}()
	endpoint := coretransport.EndpointPath(cfg.DataDir)
	listener, err := coretransport.Listen(ctx, endpoint)
	if err != nil {
		return err
	}
	runtime.coreServer, err = coretransport.NewServer(runtime.coreAPI, listener, coretransport.Config{})
	if err != nil {
		_ = listener.Close()
		return err
	}
	backgroundCtx, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	var background sync.WaitGroup
	errCh := make(chan error, 3)
	startBackground := func(name string, fn func(context.Context) error) {
		background.Add(1)
		go func() {
			defer background.Done()
			if err := fn(backgroundCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				select {
				case errCh <- fmt.Errorf("%s: %w", name, err):
				default:
				}
			}
		}()
	}
	startBackground("core api", func(workerCtx context.Context) error {
		go func() {
			<-workerCtx.Done()
			_ = runtime.coreServer.Close()
		}()
		return runtime.coreServer.Serve()
	})
	if runtime.notifier != nil {
		startBackground("matrix notifier", func(workerCtx context.Context) error {
			return runtime.notifier.Run(workerCtx, time.Second)
		})
	}
	if runtime.gateway != nil {
		startBackground("matrix sync", runtime.gateway.Run)
	}
	var healthServer *http.Server
	if runtime.health != nil && strings.TrimSpace(runtime.healthListen) != "" {
		healthServer = &http.Server{Addr: runtime.healthListen, Handler: runtime.health.Handler(), ReadHeaderTimeout: 5 * time.Second}
		startBackground("health endpoint", func(workerCtx context.Context) error {
			go func() {
				<-workerCtx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = healthServer.Shutdown(shutdownCtx)
			}()
			err := healthServer.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		})
	}
	var metricsServer *http.Server
	if runtime.metrics != nil && strings.TrimSpace(runtime.metricsListen) != "" {
		metricsServer = &http.Server{Addr: runtime.metricsListen, Handler: runtime.metrics.Handler(), ReadHeaderTimeout: 5 * time.Second}
		startBackground("metrics endpoint", func(workerCtx context.Context) error {
			go func() {
				<-workerCtx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = metricsServer.Shutdown(shutdownCtx)
			}()
			err := metricsServer.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		})
	}
	defer func() {
		cancelBackground()
		background.Wait()
	}()

	runtime.refreshMetrics(time.Now().UTC())
	runtime.logger.Record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "startup", Outcome: "ready", Resource: options.backend})
	ticker := time.NewTicker(options.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-errCh:
			return err
		default:
		}
		outcome, runErr := runtime.scheduler.RunOnce(ctx)
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) || (errors.Is(runErr, context.DeadlineExceeded) && ctx.Err() != nil) {
				return nil
			}
			return fmt.Errorf("scheduler pass: %w", runErr)
		}
		if !outcome.Idle {
			runtime.logger.Record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "scheduler", Outcome: "completed", RequestID: outcome.Request.RequestID})
		}
		runtime.refreshMetrics(time.Now().UTC())
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// cliErrorMessage is the process boundary for maintenance/startup failures.
// Internal errors can contain paths or identifiers, so the CLI reports only a
// stable category and keeps detailed values out of journals and CI logs.
func cliErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, errInvalidBackend):
		return "invalid browser backend"
	case errors.Is(err, errInvalidOptions):
		return "invalid service options"
	case errors.Is(err, config.ErrInvalidConfig):
		return "invalid configuration"
	case errors.Is(err, store.ErrInvalidRestore):
		return "invalid restore source"
	case errors.Is(err, store.ErrCorruptData):
		return "corrupt database"
	case errors.Is(err, store.ErrAccountNotFound), errors.Is(err, store.ErrRequestNotFound):
		return "requested record not found"
	case errors.Is(err, credential.ErrKeyUnavailable):
		return "credential key unavailable"
	case errors.Is(err, matrix.ErrUnauthorized):
		return "Matrix authorization failed"
	default:
		return "operation failed"
	}
}

func runDiagnostics(ctx context.Context, options serviceOptions, audit bool, auditAccount string, auditLimit int) error {
	if ctx == nil {
		return errInvalidOptions
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	database, err := store.Open(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.ValidateDatabase(); err != nil {
		return err
	}
	if audit {
		entries, err := database.ListAuditEntries(store.AuditQuery{AccountID: strings.TrimSpace(auditAccount), Limit: auditLimit})
		if err != nil {
			return err
		}
		return writeJSON(entries)
	}
	snapshot, err := database.OperationalSnapshot(time.Now().UTC())
	if err != nil {
		return err
	}
	issues, err := database.OperationalIssues(snapshot.At)
	if err != nil {
		return err
	}
	return writeJSON(struct {
		Snapshot store.OperationalSnapshot `json:"snapshot"`
		Issues   []store.OperationalIssue  `json:"issues"`
	}{Snapshot: snapshot, Issues: issues})
}

func runMaintenance(ctx context.Context, options serviceOptions, backup bool, restorePath string, injectAccount string, rotateAccount string, revokeAccount string, credentialEnv string, credentialActor string, diagnostics bool, audit bool, auditAccount string, validateBackupPath string, auditLimit int) error {
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	operations := 0
	if backup {
		operations++
	}
	if strings.TrimSpace(restorePath) != "" {
		operations++
	}
	if strings.TrimSpace(injectAccount) != "" {
		operations++
	}
	if strings.TrimSpace(rotateAccount) != "" {
		operations++
	}
	if strings.TrimSpace(revokeAccount) != "" {
		operations++
	}
	if diagnostics {
		operations++
	}
	if audit {
		operations++
	}
	if strings.TrimSpace(validateBackupPath) != "" {
		operations++
	}
	if operations != 1 {
		return fmt.Errorf("service: exactly one maintenance operation is required")
	}
	if backup {
		database, err := store.Open(cfg)
		if err != nil {
			return err
		}
		path, backupErr := database.Backup(time.Now().UTC())
		closeErr := database.Close()
		if backupErr != nil {
			return backupErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Println(path)
		return nil
	}
	if strings.TrimSpace(restorePath) != "" {
		if err := store.Restore(cfg, restorePath); err != nil {
			return err
		}
		fmt.Println("restore complete")
		return nil
	}
	if strings.TrimSpace(validateBackupPath) != "" {
		if err := store.ValidateBackup(validateBackupPath); err != nil {
			return err
		}
		fmt.Println("backup valid")
		return nil
	}
	if diagnostics || audit {
		return runDiagnostics(ctx, options, audit, auditAccount, auditLimit)
	}
	database, err := store.Open(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	keyring := credential.NewEnvKeyringWithHistory(cfg.Credentials.KeyEnv, cfg.Credentials.KeyIDEnv, cfg.Credentials.HistoryEnv)
	credentials, err := credential.New(database, keyring)
	if err != nil {
		return err
	}
	if strings.TrimSpace(credentialActor) == "" {
		credentialActor = options.owner
	}
	if strings.TrimSpace(injectAccount) != "" {
		if strings.TrimSpace(credentialEnv) == "" {
			return fmt.Errorf("service: -credential-env is required for injection")
		}
		source, sourceErr := credential.NewEnvSource(credentialEnv)
		if sourceErr != nil {
			return sourceErr
		}
		metadata, injectErr := credentials.Inject(ctx, injectAccount, credentialActor, time.Now().UTC(), source)
		if injectErr != nil {
			return injectErr
		}
		// Print metadata only; never print the injected value or ciphertext.
		fmt.Printf("credential account=%s version=%d key_id=%s\n", observability.RedactIdentifier(metadata.AccountID), metadata.Version, observability.RedactIdentifier(metadata.KeyID))
		return nil
	}
	if strings.TrimSpace(rotateAccount) != "" {
		metadata, rotateErr := credentials.Rotate(ctx, rotateAccount, credentialActor, time.Now().UTC())
		if rotateErr != nil {
			return rotateErr
		}
		fmt.Printf("credential rotated account=%s version=%d key_id=%s\n", observability.RedactIdentifier(metadata.AccountID), metadata.Version, observability.RedactIdentifier(metadata.KeyID))
		return nil
	}
	if strings.TrimSpace(revokeAccount) != "" {
		if revokeErr := credentials.Revoke(ctx, revokeAccount, credentialActor, time.Now().UTC()); revokeErr != nil {
			return revokeErr
		}
		fmt.Printf("credential revoked account=%s\n", observability.RedactIdentifier(revokeAccount))
		return nil
	}
	return errInvalidOptions
}

func main() {
	options := defaultServiceOptions()
	flag.StringVar(&options.configPath, "config", "configs/example.json", "JSON deployment configuration")
	flag.StringVar(&options.backend, "browser-backend", backendNode, "browser worker backend (node or headless)")
	flag.StringVar(&options.workerCommand, "worker-command", "node", "Node browser worker executable")
	flag.StringVar(&options.workerScript, "worker-script", "browser-worker/src/worker.mjs", "Node browser worker script")
	flag.StringVar(&options.headlessBrowserCommand, "headless-browser-command", "chromium", "externally installed Chromium/Edge executable for the headless backend")
	flag.StringVar(&options.automationAdapter, "automation-adapter", "", "explicit business adapter (genshin-cloudgame only)")
	flag.StringVar(&options.healthListen, "health-listen", "", "override the configured local health listener")
	flag.StringVar(&options.metricsListen, "metrics-listen", "", "optional local Prometheus metrics listener")
	flag.StringVar(&options.logPath, "log-path", "", "optional structured JSONL log path (defaults to stderr)")
	flag.Int64Var(&options.logMaxBytes, "log-max-bytes", 0, "maximum active structured log size before rotation (0 uses config/default)")
	flag.IntVar(&options.logMaxFiles, "log-max-files", 0, "number of rotated structured log files to retain (0 uses config/default)")
	flag.StringVar(&options.owner, "owner", "service", "scheduler and audit owner")
	flag.DurationVar(&options.pollInterval, "poll-interval", 500*time.Millisecond, "queue polling interval")
	flag.DurationVar(&options.leaseTTL, "lease-ttl", 2*time.Minute, "account lease duration")
	flag.DurationVar(&options.runTimeout, "run-timeout", 5*time.Minute, "session run timeout")
	flag.DurationVar(&options.heartbeat, "heartbeat-interval", 30*time.Second, "session lease heartbeat interval")
	flag.DurationVar(&options.cancelTimeout, "cancel-timeout", 5*time.Second, "worker cancellation timeout")
	flag.DurationVar(&options.shutdownTimeout, "shutdown-timeout", 5*time.Second, "worker shutdown timeout")
	flag.IntVar(&options.maxConcurrency, "max-concurrency", 1, "maximum concurrent account sessions")
	flag.IntVar(&options.maxAttempts, "retry-max-attempts", 3, "maximum attempts for transient failures")
	flag.DurationVar(&options.retryBaseDelay, "retry-base-delay", time.Second, "base transient retry delay")
	flag.DurationVar(&options.retryMaxDelay, "retry-max-delay", time.Minute, "maximum transient retry delay")
	selfTest := flag.Bool("self-test", false, "run the Node worker protocol smoke test and exit")
	showVersion := flag.Bool("version", false, "print the service version")
	backup := flag.Bool("backup", false, "create a timestamped database backup and exit")
	restorePath := flag.String("restore", "", "restore a validated backup under the configured backup directory and exit")
	injectAccount := flag.String("inject-account", "", "encrypt a credential from -credential-env for this account and exit")
	rotateAccount := flag.String("rotate-account", "", "re-encrypt an account credential with the current deployment key and exit")
	revokeAccount := flag.String("revoke-account", "", "invalidate and wipe an account credential and exit")
	credentialEnv := flag.String("credential-env", "", "environment variable containing one credential for -inject-account")
	credentialActor := flag.String("credential-actor", "", "audit actor for credential injection")
	diagnostics := flag.Bool("diagnostics", false, "print redaction-safe operational diagnostics and exit")
	audit := flag.Bool("audit", false, "print redaction-safe global audit entries and exit")
	auditAccount := flag.String("audit-account", "", "optional account filter for -audit")
	auditLimit := flag.Int("audit-limit", 1000, "maximum entries returned by -audit")
	validateBackupPath := flag.String("validate-backup", "", "validate a backup database without restoring it")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	if *backup || strings.TrimSpace(*restorePath) != "" || strings.TrimSpace(*injectAccount) != "" || strings.TrimSpace(*rotateAccount) != "" || strings.TrimSpace(*revokeAccount) != "" || *diagnostics || *audit || strings.TrimSpace(*validateBackupPath) != "" {
		err = runMaintenance(ctx, options, *backup, *restorePath, *injectAccount, *rotateAccount, *revokeAccount, *credentialEnv, *credentialActor, *diagnostics, *audit, *auditAccount, *validateBackupPath, *auditLimit)
	} else if *selfTest {
		err = runSelfTest(ctx, options.workerCommand, options.workerScript)
	} else {
		err = run(ctx, options)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "chuzi: %s\n", cliErrorMessage(err))
		os.Exit(1)
	}
}
