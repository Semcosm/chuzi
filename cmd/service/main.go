package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/queue"
	requestservice "github.com/Semcosm/chuzi/internal/request"
	"github.com/Semcosm/chuzi/internal/store"
)

var version = "dev"

const (
	backendNode     = "node"
	backendHeadless = "headless"
	backendRust     = "rust"
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
	browserRuntime         string
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
	store     *store.Store
	requests  *requestservice.Service
	runner    *browser.Runner
	scheduler *queue.Scheduler
}

func defaultServiceOptions() serviceOptions {
	return serviceOptions{
		configPath:             "configs/example.json",
		backend:                backendNode,
		workerCommand:          "node",
		workerScript:           "browser-worker/src/worker.mjs",
		headlessBrowserCommand: "chromium",
		browserRuntime:         "chuzi-browser-runtime",
		owner:                  "service",
		pollInterval:           500 * time.Millisecond,
		leaseTTL:               2 * time.Minute,
		runTimeout:             5 * time.Minute,
		heartbeat:              30 * time.Second,
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
	case backendRust:
		// The Rust helper is a JSONL stdin/stdout process and does not accept
		// the Node worker's script/--stdio arguments. An explicit empty, non-nil
		// argument list makes that invocation contract visible to ProcessFactory.
		return browser.NewProcessFactory(browser.ProcessConfig{
			Command: options.browserRuntime,
			Args:    []string{},
			Stderr:  workerStderr(),
		})
	case backendHeadless:
		if strings.TrimSpace(options.headlessBrowserCommand) == "" {
			return nil, fmt.Errorf("%w: empty headless browser command", errInvalidOptions)
		}
		return browser.NewProcessFactory(browser.ProcessConfig{
			Command:    options.workerCommand,
			Script:     "browser-worker/src/headless.mjs",
			ScriptArgs: []string{"--browser-command", options.headlessBrowserCommand},
			Stderr:     workerStderr(),
		})
	default:
		return nil, fmt.Errorf("%w: %q (want %s, %s, or %s)", errInvalidBackend, options.backend, backendNode, backendHeadless, backendRust)
	}
}

func (o serviceOptions) validate() error {
	if strings.TrimSpace(o.owner) == "" || o.pollInterval <= 0 || o.leaseTTL <= 0 ||
		o.runTimeout <= 0 || o.cancelTimeout <= 0 || o.shutdownTimeout <= 0 ||
		o.maxConcurrency < 1 || o.maxAttempts < 1 || o.retryBaseDelay < 0 ||
		o.retryMaxDelay < o.retryBaseDelay || o.heartbeat < 0 ||
		(o.heartbeat > 0 && o.heartbeat >= o.leaseTTL) {
		return fmt.Errorf("%w: invalid service timing or concurrency settings", errInvalidOptions)
	}
	if o.backend != backendNode && o.backend != backendHeadless && o.backend != backendRust {
		return fmt.Errorf("%w: %q (want %s, %s, or %s)", errInvalidBackend, o.backend, backendNode, backendHeadless, backendRust)
	}
	if o.backend == backendHeadless && strings.TrimSpace(o.headlessBrowserCommand) == "" {
		return fmt.Errorf("%w: empty headless browser command", errInvalidOptions)
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
	if factory == nil || now == nil {
		return nil, fmt.Errorf("%w: missing runtime dependency", errInvalidOptions)
	}
	database, err := store.Open(cfg)
	if err != nil {
		return nil, err
	}
	closeOnError := func(closeErr error) (*serviceRuntime, error) {
		_ = database.Close()
		return nil, closeErr
	}

	profiles, err := browser.NewProfiles(cfg)
	if err != nil {
		return closeOnError(err)
	}
	requestService, err := requestservice.New(database, now, requestservice.IDGenerator(newID), options.owner)
	if err != nil {
		return closeOnError(err)
	}
	sessionRunner, err := browser.New(factory, database, profiles, browser.Config{
		LeaseTTL:          options.leaseTTL,
		HeartbeatInterval: options.heartbeat,
		CancelTimeout:     options.cancelTimeout,
		ShutdownTimeout:   options.shutdownTimeout,
		Clock:             now,
	})
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
	})
	if err != nil {
		return closeOnError(err)
	}
	return &serviceRuntime{
		store:     database,
		requests:  requestService,
		runner:    sessionRunner,
		scheduler: scheduler,
	}, nil
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
	runtime, err := assembleRuntime(cfg, options, time.Now)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := runtime.store.Close(); closeErr != nil {
			log.Printf("close store: %v", closeErr)
		}
	}()

	log.Printf("chuzi service %s is ready; backend=%s data_dir=%s", version, options.backend, cfg.DataDir)
	ticker := time.NewTicker(options.pollInterval)
	defer ticker.Stop()
	for {
		outcome, runErr := runtime.scheduler.RunOnce(ctx)
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) || (errors.Is(runErr, context.DeadlineExceeded) && ctx.Err() != nil) {
				return nil
			}
			return fmt.Errorf("scheduler pass: %w", runErr)
		}
		if !outcome.Idle {
			log.Printf("scheduler completed request=%s succeeded=%t retried=%t", outcome.Request.RequestID, outcome.Succeeded, outcome.Retried)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func main() {
	options := defaultServiceOptions()
	flag.StringVar(&options.configPath, "config", "configs/example.json", "JSON deployment configuration")
	flag.StringVar(&options.backend, "browser-backend", backendNode, "browser worker backend (node, headless, or rust)")
	flag.StringVar(&options.workerCommand, "worker-command", "node", "Node browser worker executable")
	flag.StringVar(&options.workerScript, "worker-script", "browser-worker/src/worker.mjs", "Node browser worker script")
	flag.StringVar(&options.headlessBrowserCommand, "headless-browser-command", "chromium", "externally installed Chromium/Edge executable for the headless backend")
	flag.StringVar(&options.browserRuntime, "browser-runtime", "chuzi-browser-runtime", "Rust browser runtime executable")
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
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	if *selfTest {
		err = runSelfTest(ctx, options.workerCommand, options.workerScript)
	} else {
		err = run(ctx, options)
	}
	if err != nil {
		log.Fatal(err)
	}
}
