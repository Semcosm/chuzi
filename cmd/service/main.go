package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	adapterpkg "github.com/Semcosm/chuzi/internal/adapter"
	"github.com/Semcosm/chuzi/internal/automation"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/core"
	"github.com/Semcosm/chuzi/internal/coreapi"
	"github.com/Semcosm/chuzi/internal/coretransport"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/diagnostics"
	"github.com/Semcosm/chuzi/internal/environment"
	"github.com/Semcosm/chuzi/internal/health"
	"github.com/Semcosm/chuzi/internal/matrix"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/plugin"
	"github.com/Semcosm/chuzi/internal/queue"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/slot"
	"github.com/Semcosm/chuzi/internal/slotwindows"
	"github.com/Semcosm/chuzi/internal/store"
)

var version = "dev"

const (
	backendNode     = "node"
	backendHeadless = "headless"
	backendHeaded   = "headed"
)

var (
	errInvalidBackend     = errors.New("service: invalid browser backend")
	errInvalidOptions     = errors.New("service: invalid runtime options")
	errAdapterUnavailable = errors.New("service: automation adapter unavailable")
	serviceSequence       atomic.Uint64
)

type serviceOptions struct {
	configPath             string
	backend                string
	workerCommand          string
	workerScript           string
	headlessBrowserCommand string
	windowsDesktop         string
	windowsLauncherCommand string
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
	// sessionBootstrapper is an internal deployment seam. Windows production
	// assembly supplies the fixed broker client; tests may inject a fake
	// provider without exposing that seam through ordinary configuration.
	sessionBootstrapper slotwindows.SessionBootstrapper
}

type serviceRuntime struct {
	store              *store.Store
	environment        *environment.Manager
	environmentID      string
	environmentVersion string
	jobPoolID          string
	requests           *requestservice.Service
	credentials        *credential.Service
	runner             queue.Runner
	automation         automation.Adapter
	scheduler          *queue.Scheduler
	notifier           *matrix.Notifier
	gateway            *matrix.Gateway
	matrixClient       *matrix.HTTPClient
	health             *health.Checker
	healthListen       string
	metrics            *observability.Metrics
	logger             *observability.JSONLogger
	diagnostics        *diagnostics.Service
	eventBuffer        *observability.EventBuffer
	metricsListen      string
	coreAPI            coreapi.API
	coreServer         *coretransport.Server
	slotReconciler     slotReconciler
	slotInterval       time.Duration
	slotHealth         *slotLifecycleHealth
	rdp                *rdpCapabilityBridge
}

func (r *serviceRuntime) record(event observability.Event) {
	if r == nil {
		return
	}
	observability.MultiSink{r.metrics, r.logger, r.eventBuffer}.Record(event)
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
	r.metrics.Set("chuzi_job_pools", float64(snapshot.JobPools))
	r.metrics.Set("chuzi_slots_desired", float64(snapshot.DesiredSlots))
	r.metrics.Set("chuzi_slots_ready", float64(snapshot.ReadySlots))
	r.metrics.Set("chuzi_slots_leased", float64(snapshot.LeasedSlots))
	r.metrics.Set("chuzi_slots_quarantined", float64(snapshot.QuarantinedSlots))
	r.metrics.Set("chuzi_slots_draining", float64(snapshot.DrainingSlots))
	r.metrics.Set("chuzi_slots_provisioning", float64(snapshot.ProvisioningSlots))
	r.metrics.Set("chuzi_slots_retiring", float64(snapshot.RetiringSlots))
	r.metrics.Set("chuzi_slots_unprovisioned", float64(snapshot.UnprovisionedSlots))
}

func (r *serviceRuntime) recordSlotReconcile(at time.Time, err error) {
	if r == nil {
		return
	}
	if r.slotHealth != nil {
		r.slotHealth.set(err)
	}
	if err == nil {
		return
	}
	event := observability.Event{
		At:        at.UTC(),
		Component: "slot",
		Operation: "reconcile",
		Resource:  "execution_slots",
	}
	event.Outcome = "failed"
	event.ErrorClass = slotReconcileErrorClass(err)
	if r.metrics != nil {
		r.metrics.Inc("chuzi_slot_reconcile_errors_total")
	}
	observability.MultiSink{r.metrics, r.logger, r.eventBuffer}.Record(event)
}

func slotReconcileErrorClass(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "trusted environment"):
		return "trust"
	case strings.Contains(message, "cleanup"):
		return "cleanup"
	case strings.Contains(message, "provisioning"), strings.Contains(message, "session"), strings.Contains(message, "health"):
		return "health"
	case strings.Contains(message, "permission"):
		return "permission"
	case strings.Contains(message, "config"):
		return "configuration"
	default:
		return "reconcile_failed"
	}
}

func defaultServiceOptions() serviceOptions {
	return serviceOptions{
		configPath:             "configs/example.json",
		backend:                backendHeaded,
		workerCommand:          "node",
		workerScript:           "browser-worker/src/worker.mjs",
		headlessBrowserCommand: "chromium",
		windowsLauncherCommand: "chuzi-browser-launcher.exe",
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
	case backendHeadless, backendHeaded:
		if strings.TrimSpace(options.headlessBrowserCommand) == "" {
			return nil, fmt.Errorf("%w: empty headless browser command", errInvalidOptions)
		}
		workerMode := ""
		if strings.TrimSpace(options.automationAdapter) != "" {
			workerMode = "adapter"
		}
		browserMode := backendHeadless
		if options.backend == backendHeaded {
			browserMode = backendHeaded
		}
		scriptArgs := []string{"--browser-command", options.headlessBrowserCommand, "--browser-mode", browserMode, "--windows-launcher-command", options.windowsLauncherCommand}
		if strings.TrimSpace(options.windowsDesktop) != "" {
			scriptArgs = append(scriptArgs, "--windows-desktop", options.windowsDesktop)
		}
		return browser.NewProcessFactory(browser.ProcessConfig{
			Command:    options.workerCommand,
			Script:     "browser-worker/src/headless.mjs",
			ScriptArgs: scriptArgs,
			Stderr:     workerStderr(),
			WorkerMode: workerMode,
		})
	default:
		return nil, fmt.Errorf("%w: %q (want %s, %s, or %s)", errInvalidBackend, options.backend, backendNode, backendHeadless, backendHeaded)
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

func serviceTarget() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "windows-amd64"
	case "linux/amd64":
		return "linux-amd64"
	case "linux/arm64":
		return "linux-arm64"
	case "darwin/arm64":
		return "darwin-arm64"
	default:
		return ""
	}
}

func resolveAutomationPackage(cfg config.Config, id string) (adapterpkg.Package, error) {
	states, err := adapterpkg.LoadStates(filepath.Join(cfg.DataDir, ".chuzi", "launcher-state.json"))
	if err != nil {
		return adapterpkg.Package{}, fmt.Errorf("%w: %w", errAdapterUnavailable, err)
	}
	registry, err := adapterpkg.NewRegistry(filepath.Join(cfg.DataDir, "plugins"), serviceTarget(), states)
	if err != nil {
		return adapterpkg.Package{}, fmt.Errorf("%w: %w", errAdapterUnavailable, err)
	}
	resolved, err := registry.Resolve(id)
	if err != nil {
		return adapterpkg.Package{}, fmt.Errorf("%w: %w", errAdapterUnavailable, err)
	}
	return resolved, nil
}

func (o serviceOptions) validate() error {
	if strings.TrimSpace(o.owner) == "" || o.pollInterval <= 0 || o.leaseTTL <= 0 ||
		o.runTimeout <= 0 || o.cancelTimeout <= 0 || o.shutdownTimeout <= 0 ||
		o.maxConcurrency < 1 || o.maxAttempts < 1 || o.retryBaseDelay < 0 ||
		o.retryMaxDelay < o.retryBaseDelay || o.heartbeat < 0 ||
		(o.heartbeat > 0 && o.heartbeat >= o.leaseTTL) {
		return fmt.Errorf("%w: invalid service timing or concurrency settings", errInvalidOptions)
	}
	if o.backend != backendNode && o.backend != backendHeadless && o.backend != backendHeaded {
		return fmt.Errorf("%w: %q (want %s, %s, or %s)", errInvalidBackend, o.backend, backendNode, backendHeadless, backendHeaded)
	}
	if (o.backend == backendHeadless || o.backend == backendHeaded) && strings.TrimSpace(o.headlessBrowserCommand) == "" {
		return fmt.Errorf("%w: empty headless browser command", errInvalidOptions)
	}
	if strings.TrimSpace(o.windowsDesktop) != "" && strings.TrimSpace(o.windowsLauncherCommand) == "" {
		return fmt.Errorf("%w: empty Windows browser launcher command", errInvalidOptions)
	}
	if strings.TrimSpace(o.automationAdapter) != "" &&
		(strings.TrimSpace(o.automationAdapter) != "genshin-cloudgame" || (o.backend != backendHeadless && o.backend != backendHeaded)) {
		return fmt.Errorf("%w: genshin-cloudgame requires a CDP backend", errInvalidOptions)
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
	var environmentManager *environment.Manager
	var environmentRuntime *serviceEnvironmentRuntime
	var poolConfig slot.PoolConfig
	if cfg.JobPool.Enabled() {
		poolConfig = slot.PoolConfig{
			PoolID:             cfg.JobPool.PoolID,
			EnvironmentID:      cfg.JobPool.EnvironmentID,
			EnvironmentVersion: cfg.JobPool.EnvironmentVersion,
			DesiredSlots:       cfg.JobPool.DesiredSlots,
			Capabilities:       append([]string(nil), cfg.JobPool.Capabilities...),
			ManifestDigest:     cfg.JobPool.ManifestDigest,
			Signer:             cfg.JobPool.Signer,
			RequireTrusted:     cfg.JobPool.RequireTrusted,
		}
	}
	if cfg.WindowsJobPool.Enabled {
		environmentManager, err = newConfiguredEnvironmentManager(cfg, serviceTarget())
		if err != nil {
			_ = database.Close()
			return nil, err
		}
		if err := environmentManager.SyncRecords(database); err != nil {
			_ = database.Close()
			return nil, err
		}
	}
	// A crash can leave a package operation in requested/provisioning. Resolve
	// that durable state before exposing the Core endpoint on restart.
	if err := database.RecoverEnvironmentOperations(now()); err != nil {
		_ = database.Close()
		return nil, err
	}
	if cfg.JobPool.Enabled() {
		existing, getErr := database.GetJobPool(poolConfig.PoolID)
		if getErr == nil {
			// The durable control-plane revision is authoritative after the first
			// boot. A restart must not overwrite it from deployment config.
			poolConfig = existing
		} else if errors.Is(getErr, slot.ErrPoolNotFound) {
			deleted, deletionErr := database.JobPoolDeletionCompleted(poolConfig.PoolID)
			if deletionErr != nil {
				_ = database.Close()
				return nil, deletionErr
			}
			if deleted {
				// A completed delete is durable intent. Keep the static deployment
				// template from recreating the pool on restart and avoid requiring a
				// package runtime for a pool that no longer exists.
				poolConfig = slot.PoolConfig{}
			} else {
				err = database.ReconcileTrustedJobPool(poolConfig, now())
			}
			if err != nil {
				_ = database.Close()
				return nil, err
			}
		} else {
			_ = database.Close()
			return nil, getErr
		}
	}
	if cfg.WindowsJobPool.Enabled {
		entryName := "headless"
		if options.backend == backendNode {
			entryName = "worker"
		}
		environmentRuntime, err = resolveServiceEnvironment(cfg, environmentManager, poolConfig, entryName)
		if err != nil {
			_ = database.Close()
			return nil, err
		}
	}
	// Slot lease recovery runs from Scheduler.RunOnce after the provisioner
	// and capability revoker are wired, so stale agents are fenced before the
	// durable lease is removed.
	var rdpBridge *rdpCapabilityBridge
	if cfg.WindowsJobPool.Enabled && cfg.WindowsJobPool.RDPEnabled {
		rdpBridge, err = newRDPCapabilityBridge(now)
		if err != nil {
			_ = database.Close()
			return nil, err
		}
	}
	slotReconciler, slotProfileAccess, err := newSlotReconciler(cfg, options, database, now, rdpBridge, environmentRuntime, poolConfig)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	factory, err = configureWorkerFactory(cfg, options, factory, slotProfileAccess)
	if err != nil {
		_ = database.Close()
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
		{Name: "chuzi_slot_reconcile_errors_total", Help: "Slot lifecycle reconciliation failures", Kind: observability.Counter},
		{Name: "chuzi_rate_limited_requests_total", Help: "New request submissions rejected by service rate limits", Kind: observability.Counter},
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
		{Name: "chuzi_job_pools", Help: "Configured logical job pools", Kind: observability.Gauge},
		{Name: "chuzi_slots_desired", Help: "Desired logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_ready", Help: "Ready logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_leased", Help: "Leased logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_quarantined", Help: "Quarantined logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_draining", Help: "Draining logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_provisioning", Help: "Provisioning logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_retiring", Help: "Retiring logical execution slots", Kind: observability.Gauge},
		{Name: "chuzi_slots_unprovisioned", Help: "Unprovisioned logical execution slots", Kind: observability.Gauge},
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
	eventBuffer := observability.NewEventBuffer(256)
	sink := observability.MultiSink{metrics, logger, eventBuffer}
	queueDir := cfg.Diagnostics.QueueDir
	if strings.TrimSpace(queueDir) == "" {
		queueDir = filepath.Join(cfg.DataDir, "diagnostics")
	} else if !filepath.IsAbs(queueDir) {
		queueDir = filepath.Join(cfg.DataDir, queueDir)
	}
	diagnosticService, diagnosticsErr := diagnostics.New(diagnostics.Config{
		Enabled:        cfg.Diagnostics.Enabled,
		Endpoint:       cfg.Diagnostics.Endpoint,
		QueueDir:       queueDir,
		MaxReportBytes: cfg.Diagnostics.MaxReportBytes,
		MaxQueueFiles:  cfg.Diagnostics.MaxQueueFiles,
		RetryBase:      time.Duration(cfg.Diagnostics.RetryBaseSeconds) * time.Second,
		RetryMax:       time.Duration(cfg.Diagnostics.RetryMaxSeconds) * time.Second,
		Version:        version,
		Events:         func() []observability.Event { return eventBuffer.Snapshot(256) },
		Clock:          now,
	})
	if diagnosticsErr != nil {
		return closeOnError(diagnosticsErr)
	}

	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		return closeOnError(err)
	}
	viewRegistry := browser.NewViewRegistry()
	requestService, err := requestservice.NewWithConfig(database, now, requestservice.IDGenerator(newID), options.owner, requestservice.Config{
		RateLimit: requestservice.RateLimitConfig{
			GlobalLimit: cfg.RateLimit.GlobalLimit, GlobalWindow: time.Duration(cfg.RateLimit.GlobalWindowSeconds) * time.Second,
			ActorLimit: cfg.RateLimit.ActorLimit, ActorWindow: time.Duration(cfg.RateLimit.ActorWindowSeconds) * time.Second,
			RoomLimit: cfg.RateLimit.RoomLimit, RoomWindow: time.Duration(cfg.RateLimit.RoomWindowSeconds) * time.Second,
			AccountLimit: cfg.RateLimit.AccountLimit, AccountWindow: time.Duration(cfg.RateLimit.AccountWindowSeconds) * time.Second,
		},
		Sink:         sink,
		Capabilities: rdpBridge,
	})
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
		packageAdapter, packageErr := resolveAutomationPackage(cfg, strings.TrimSpace(options.automationAdapter))
		if packageErr != nil {
			return closeOnError(errAdapterUnavailable)
		}
		node, lookErr := resolveServiceWorkerCommand(options, cfg.WindowsJobPool.Enabled)
		if lookErr != nil {
			return closeOnError(fmt.Errorf("service: automation adapter runtime unavailable"))
		}
		adapterPath := packageAdapter.Entry
		adapterMode := backendHeadless
		if options.backend == backendHeaded {
			adapterMode = backendHeaded
		}
		adapterArgs := []string{adapterPath, "--browser-command", options.headlessBrowserCommand, "--browser-mode", adapterMode, "--windows-launcher-command", options.windowsLauncherCommand}
		if strings.TrimSpace(options.windowsDesktop) != "" {
			adapterArgs = append(adapterArgs, "--windows-desktop", options.windowsDesktop)
		}
		client, startErr := plugin.StartAdapter(context.Background(), plugin.Command{
			Mode: plugin.Native, Executable: node,
			Args:   adapterArgs,
			Stderr: workerStderr(),
		})
		if startErr != nil {
			return closeOnError(fmt.Errorf("service: automation adapter unavailable"))
		}
		automationAdapter = client
		descriptor, describeErr := client.Describe(context.Background())
		if describeErr != nil || descriptor.ID != packageAdapter.Manifest.ID || descriptor.API != adapterpkg.AdapterAPI || descriptor.Version != packageAdapter.Manifest.Version || !hasCapability(descriptor, "genshin-cloudgame", "1") {
			return closeOnError(errAdapterUnavailable)
		}
		pipelineRunner, pipelineErr := core.NewPipelineRunner(database, core.PipelineConfig{
			Factory: factory, Credentials: credentials, Automation: client, Profiles: profiles,
			Browser: browser.Config{LeaseTTL: options.leaseTTL, HeartbeatInterval: options.heartbeat, CancelTimeout: options.cancelTimeout, ShutdownTimeout: options.shutdownTimeout, WorkerMode: "adapter", ViewRegistry: viewRegistry, CancellationObserver: database, SlotLeases: database, ProfileAccess: slotProfileAccess, Clock: now, Sink: sink},
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
			LeaseTTL:             options.leaseTTL,
			HeartbeatInterval:    options.heartbeat,
			CancelTimeout:        options.cancelTimeout,
			ShutdownTimeout:      options.shutdownTimeout,
			ViewRegistry:         viewRegistry,
			CancellationObserver: database,
			SlotLeases:           database,
			ProfileAccess:        slotProfileAccess,
			Clock:                now,
			Sink:                 sink,
		})
	}
	if err != nil {
		return closeOnError(err)
	}
	var slotLeaseStopper queue.SlotLeaseStopper
	if candidate, ok := slotProfileAccess.(queue.SlotLeaseStopper); ok {
		slotLeaseStopper = candidate
	}
	staticRuntimeConfig := queue.RuntimeConfig{SlotPoolID: poolConfig.PoolID, SlotRequirement: poolConfig.Requirement(), MaxGlobalConcurrency: options.maxConcurrency}
	runtimeConfig := func() (queue.RuntimeConfig, error) {
		if staticRuntimeConfig.SlotPoolID == "" {
			return staticRuntimeConfig, nil
		}
		current, getErr := database.GetJobPool(staticRuntimeConfig.SlotPoolID)
		if getErr != nil {
			if errors.Is(getErr, slot.ErrPoolNotFound) {
				// A completed delete removes the pool config after all platform
				// slots are retired. Stop selecting that pool without failing the
				// scheduler loop or reviving it from startup configuration.
				return queue.RuntimeConfig{MaxGlobalConcurrency: options.maxConcurrency}, nil
			}
			return queue.RuntimeConfig{}, getErr
		}
		maxConcurrency := current.MaxConcurrency
		if maxConcurrency < 1 {
			maxConcurrency = options.maxConcurrency
		}
		return queue.RuntimeConfig{SlotPoolID: current.PoolID, SlotRequirement: current.Requirement(), MaxGlobalConcurrency: maxConcurrency}, nil
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
		Clock:            now,
		NewID:            newID,
		Sink:             sink,
		SlotPoolID:       staticRuntimeConfig.SlotPoolID,
		SlotRequirement:  staticRuntimeConfig.SlotRequirement,
		RuntimeConfig:    runtimeConfig,
		Capabilities:     rdpBridge,
		SlotLeaseStopper: slotLeaseStopper,
	})
	if err != nil {
		return closeOnError(err)
	}
	var environmentExecutor core.EnvironmentExecutor
	var environmentControl core.EnvironmentControlPort = database
	if environmentManager != nil {
		environmentExecutor = serviceEnvironmentExecutor{manager: environmentManager, store: database}
		environmentControl = serviceEnvironmentControl{manager: environmentManager, store: database}
	}
	coreAPI, coreErr := core.New(core.Dependencies{Requests: requestService, Store: database, Views: viewRegistry, RDP: rdpBridge, Diagnostics: diagnosticService, JobPools: database, JobPoolControl: database, SlotSessions: database, Environments: environmentControl, EnvironmentExecutor: environmentExecutor, JobPoolID: poolConfig.PoolID, MaxConcurrency: options.maxConcurrency, Clock: now})
	if coreErr != nil {
		return closeOnError(coreErr)
	}
	runtime := &serviceRuntime{
		store: database, environment: environmentManager, environmentID: poolConfig.EnvironmentID, environmentVersion: poolConfig.EnvironmentVersion, jobPoolID: poolConfig.PoolID, requests: requestService, credentials: credentials,
		runner: sessionRunner, automation: automationAdapter, scheduler: scheduler, healthListen: cfg.Health.Listen, metrics: metrics, logger: logger, diagnostics: diagnosticService, eventBuffer: eventBuffer, coreAPI: coreAPI,
		rdp: rdpBridge,
	}
	runtime.slotReconciler = slotReconciler
	if slotReconciler != nil {
		runtime.slotHealth = newSlotLifecycleHealth()
	}
	runtime.slotInterval = 5 * time.Second
	if cfg.WindowsJobPool.Enabled {
		runtime.slotInterval = time.Duration(cfg.WindowsJobPool.AgentHeartbeatSeconds) * time.Second
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
			adapter, adapterErr := matrix.NewAdapter(requestService, policy, matrix.Config{UserID: cfg.Matrix.UserID, Clock: now, Sink: sink})
			if adapterErr != nil {
				return closeOnError(adapterErr)
			}
			syncTimeout := time.Duration(cfg.Matrix.SyncTimeoutSeconds) * time.Second
			if syncTimeout == 0 {
				syncTimeout = 30 * time.Second
			}
			pollInterval := time.Duration(cfg.Matrix.PollIntervalMillis) * time.Millisecond
			gateway, gatewayErr := matrix.NewGateway(matrix.GatewayConfig{Client: client, Adapter: adapter, CursorStore: database, SyncTimeout: syncTimeout, PollInterval: pollInterval})
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
	if runtime.slotHealth != nil {
		probes["slot_lifecycle"] = runtime.slotHealth.probe
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

func main() {
	options := defaultServiceOptions()
	flag.StringVar(&options.configPath, "config", "configs/example.json", "JSON deployment configuration")
	flag.StringVar(&options.backend, "browser-backend", backendHeaded, "browser worker backend (node, headless, or headed)")
	flag.StringVar(&options.workerCommand, "worker-command", "node", "Node browser worker executable")
	flag.StringVar(&options.workerScript, "worker-script", "browser-worker/src/worker.mjs", "Node browser worker script")
	flag.StringVar(&options.headlessBrowserCommand, "browser-command", "chromium", "externally installed Chromium/Edge executable for the CDP backend")
	flag.StringVar(&options.headlessBrowserCommand, "headless-browser-command", "chromium", "deprecated alias for -browser-command")
	flag.StringVar(&options.windowsDesktop, "windows-desktop", "", "Windows desktop name for headed browser launch in the current session")
	flag.StringVar(&options.windowsLauncherCommand, "windows-launcher-command", "chuzi-browser-launcher.exe", "Windows helper used to launch a browser on the selected desktop")
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
	environmentInstall := flag.String("environment-install", "", "install a signed environment package source")
	environmentUpgrade := flag.String("environment-upgrade", "", "upgrade from a signed environment package source")
	environmentTrust := flag.Bool("environment-trust", false, "trust an installed environment")
	environmentEnable := flag.Bool("environment-enable", false, "enable a trusted environment")
	environmentDisable := flag.Bool("environment-disable", false, "disable an environment")
	environmentHealth := flag.Bool("environment-health", false, "health-check an enabled environment")
	environmentRollback := flag.Bool("environment-rollback", false, "rollback an environment package")
	environmentPromote := flag.Bool("environment-promote", false, "promote manager-ready environment records into Store")
	environmentID := flag.String("environment-id", "", "environment ID for a lifecycle operation")
	environmentVersion := flag.String("environment-version", "", "environment version for a lifecycle operation")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	environmentOperation := ""
	environmentSource := ""
	for operation, enabled := range map[string]bool{"install": strings.TrimSpace(*environmentInstall) != "", "upgrade": strings.TrimSpace(*environmentUpgrade) != "", "trust": *environmentTrust, "enable": *environmentEnable, "disable": *environmentDisable, "health": *environmentHealth, "rollback": *environmentRollback, "promote": *environmentPromote} {
		if enabled {
			if environmentOperation != "" {
				err = fmt.Errorf("service: multiple environment operations")
				break
			}
			environmentOperation = operation
		}
	}
	if environmentOperation == "install" {
		environmentSource = *environmentInstall
	} else if environmentOperation == "upgrade" {
		environmentSource = *environmentUpgrade
	}
	if err == nil && environmentOperation != "" {
		err = runEnvironmentMaintenance(ctx, options, environmentOperation, environmentSource, *environmentID, *environmentVersion)
	} else if err == nil && (*backup || strings.TrimSpace(*restorePath) != "" || strings.TrimSpace(*injectAccount) != "" || strings.TrimSpace(*rotateAccount) != "" || strings.TrimSpace(*revokeAccount) != "" || *diagnostics || *audit || strings.TrimSpace(*validateBackupPath) != "") {
		err = runMaintenance(ctx, options, *backup, *restorePath, *injectAccount, *rotateAccount, *revokeAccount, *credentialEnv, *credentialActor, *diagnostics, *audit, *auditAccount, *validateBackupPath, *auditLimit)
	} else if err == nil && *selfTest {
		err = runSelfTest(ctx, options.workerCommand, options.workerScript)
	} else {
		err = run(ctx, options)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "chuzi: %s\n", cliErrorMessage(err))
		os.Exit(1)
	}
}
