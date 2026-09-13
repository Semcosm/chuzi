package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Semcosm/chuzi/internal/account"
	"github.com/Semcosm/chuzi/internal/browser"
	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/credential"
	"github.com/Semcosm/chuzi/internal/health"
	"github.com/Semcosm/chuzi/internal/matrix"
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
	healthListen           string
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
	store        *store.Store
	requests     *requestservice.Service
	credentials  *credential.Service
	runner       *browser.Runner
	scheduler    *queue.Scheduler
	notifier     *matrix.Notifier
	gateway      *matrix.Gateway
	matrixClient *matrix.HTTPClient
	health       *health.Checker
	healthListen string
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
	keyring := credential.NewEnvKeyring(cfg.Credentials.KeyEnv, cfg.Credentials.KeyIDEnv)
	credentials, err := credential.New(database, keyring)
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
	runtime := &serviceRuntime{
		store: database, requests: requestService, credentials: credentials,
		runner: sessionRunner, scheduler: scheduler, healthListen: cfg.Health.Listen,
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
			RetryMax: time.Minute, BatchSize: 32, Clock: now,
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
	checker, checkerErr := health.NewChecker(probes)
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
		if closeErr := runtime.store.Close(); closeErr != nil {
			log.Printf("close store: %v", closeErr)
		}
	}()
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
	defer func() {
		cancelBackground()
		background.Wait()
	}()

	log.Printf("chuzi service %s is ready; backend=%s data_dir=%s", version, options.backend, cfg.DataDir)
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
			log.Printf("scheduler completed request=%s succeeded=%t retried=%t", outcome.Request.RequestID, outcome.Succeeded, outcome.Retried)
		}
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runMaintenance(ctx context.Context, options serviceOptions, backup bool, restorePath, injectAccount, credentialEnv, credentialActor string) error {
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
	database, err := store.Open(cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	keyring := credential.NewEnvKeyring(cfg.Credentials.KeyEnv, cfg.Credentials.KeyIDEnv)
	credentials, err := credential.New(database, keyring)
	if err != nil {
		return err
	}
	if strings.TrimSpace(credentialEnv) == "" {
		return fmt.Errorf("service: -credential-env is required for injection")
	}
	source, err := credential.NewEnvSource(credentialEnv)
	if err != nil {
		return err
	}
	if strings.TrimSpace(credentialActor) == "" {
		credentialActor = options.owner
	}
	metadata, err := credentials.Inject(ctx, injectAccount, credentialActor, time.Now().UTC(), source)
	if err != nil {
		return err
	}
	// Print metadata only; never print the injected value or ciphertext.
	fmt.Printf("credential account=%s version=%d key_id=%s\n", metadata.AccountID, metadata.Version, metadata.KeyID)
	return nil
}

func main() {
	options := defaultServiceOptions()
	flag.StringVar(&options.configPath, "config", "configs/example.json", "JSON deployment configuration")
	flag.StringVar(&options.backend, "browser-backend", backendNode, "browser worker backend (node, headless, or rust)")
	flag.StringVar(&options.workerCommand, "worker-command", "node", "Node browser worker executable")
	flag.StringVar(&options.workerScript, "worker-script", "browser-worker/src/worker.mjs", "Node browser worker script")
	flag.StringVar(&options.headlessBrowserCommand, "headless-browser-command", "chromium", "externally installed Chromium/Edge executable for the headless backend")
	flag.StringVar(&options.browserRuntime, "browser-runtime", "chuzi-browser-runtime", "Rust browser runtime executable")
	flag.StringVar(&options.healthListen, "health-listen", "", "override the configured local health listener")
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
	credentialEnv := flag.String("credential-env", "", "environment variable containing one credential for -inject-account")
	credentialActor := flag.String("credential-actor", "", "audit actor for credential injection")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	if *backup || strings.TrimSpace(*restorePath) != "" || strings.TrimSpace(*injectAccount) != "" {
		err = runMaintenance(ctx, options, *backup, *restorePath, *injectAccount, *credentialEnv, *credentialActor)
	} else if *selfTest {
		err = runSelfTest(ctx, options.workerCommand, options.workerScript)
	} else {
		err = run(ctx, options)
	}
	if err != nil {
		log.Fatal(err)
	}
}
