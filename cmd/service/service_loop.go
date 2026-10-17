package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/observability"
	"github.com/Semcosm/chuzi/internal/servicehost"
	"github.com/Semcosm/chuzi/internal/slot"
)

// run owns the long-lived service loop. Runtime assembly and maintenance
// commands live in separate modules so this supervisor only coordinates
// lifecycle, endpoints, and scheduling.
func run(ctx context.Context, options serviceOptions) (runErr error) {
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
		if runtime.rdp != nil {
			_ = runtime.rdp.Close()
		}
		if runtime.coreServer != nil {
			_ = runtime.coreServer.Close()
		}
		if runtime.automation != nil {
			_ = runtime.automation.Close(context.Background())
		}
		if closeErr := runtime.store.Close(); closeErr != nil {
			runtime.record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "failed", ErrorClass: "store_close_failed"})
		}
		if runtime.logger != nil {
			runtime.record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "stopping"})
			_ = runtime.logger.Close()
		}
	}()
	endpoints, err := wireEndpointRunners(ctx, runtime, cfg)
	if err != nil {
		return err
	}
	backgroundCtx, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	var background sync.WaitGroup
	errCh := make(chan error, 8)
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
	for _, endpoint := range endpoints {
		endpoint := endpoint
		startBackground(endpoint.name, endpoint.run)
	}
	if runtime.notifier != nil {
		startBackground("matrix notifier", func(workerCtx context.Context) error {
			return runtime.notifier.Run(workerCtx, time.Second)
		})
	}
	if runtime.gateway != nil {
		startBackground("matrix sync", runtime.gateway.Run)
	}
	if runtime.diagnostics != nil {
		startBackground("diagnostics uploader", func(workerCtx context.Context) error {
			_ = runtime.diagnostics.Flush(workerCtx)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return nil
				case <-ticker.C:
					_ = runtime.diagnostics.Flush(workerCtx)
				}
			}
		})
	}
	if controller, ok := runtime.slotReconciler.(slotOperationReconciler); ok {
		startBackground("slot session operations", func(workerCtx context.Context) error {
			return runSlotSessionController(workerCtx, controller, options.owner+"-session", runtime.slotInterval, func(controllerErr error) {
				runtime.record(observability.Event{At: time.Now().UTC(), Component: "slot", Operation: "session_controller", Outcome: "failed", ErrorClass: slotReconcileErrorClass(controllerErr)})
			})
		})
	}
	if runtime.slotReconciler != nil || runtime.jobPoolID != "" {
		startBackground("slot lifecycle", func(workerCtx context.Context) error {
			reconcile := func() {
				var err error
				if _, pruneErr := runtime.store.PruneSlotSessionOperations(time.Now().UTC(), 1024, 7*24*time.Hour); pruneErr != nil {
					err = pruneErr
				}
				if sessionErr := runtime.store.ReconcileSlotSessionOperations(time.Now().UTC()); sessionErr != nil {
					if err == nil {
						err = sessionErr
					}
				}
				if allPools, ok := runtime.slotReconciler.(allPoolReconciler); ok {
					if err == nil {
						err = allPools.ReconcileAll(workerCtx)
					}
					runtime.recordSlotReconcile(time.Now().UTC(), err)
					return
				}
				poolExists := true
				environmentID, environmentVersion := runtime.environmentID, runtime.environmentVersion
				if runtime.jobPoolID != "" {
					if current, getErr := runtime.store.GetJobPool(runtime.jobPoolID); getErr == nil {
						environmentID, environmentVersion = current.EnvironmentID, current.EnvironmentVersion
					} else if errors.Is(getErr, slot.ErrPoolNotFound) {
						// Delete completed only after slot cleanup. The reconciler and
						// environment health loop have no work once the config is gone.
						poolExists = false
					} else {
						err = getErr
					}
					if poolExists {
						if _, controlErr := runtime.store.ReconcileJobPoolControl(runtime.jobPoolID, time.Now().UTC()); controlErr != nil {
							err = controlErr
						}
					}
				}
				if poolExists && runtime.environment != nil {
					// Revalidate the signed tree before each slot pass. Store remains
					// the projection used by claims and capacity, so an external
					// package edit immediately removes readiness from both views.
					if _, healthErr := runtime.environment.HealthCheck(workerCtx, environmentID, environmentVersion); healthErr != nil {
						err = healthErr
					}
					// Mirror health changes before the next control reconcile.
					if syncErr := runtime.environment.SyncRecords(runtime.store); syncErr != nil && err == nil {
						err = syncErr
					}
				}
				if err == nil && poolExists && runtime.slotReconciler != nil {
					err = runtime.slotReconciler.Reconcile(workerCtx)
				}
				runtime.recordSlotReconcile(time.Now().UTC(), err)
			}
			reconcile()
			interval := runtime.slotInterval
			if interval <= 0 {
				interval = 5 * time.Second
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return nil
				case <-ticker.C:
					reconcile()
				}
			}
		})
	}
	defer func() {
		limit := options.shutdownTimeout
		if limit <= 0 {
			limit = 5 * time.Second
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), limit)
		defer shutdownCancel()
		result := servicehost.Shutdown(shutdownCtx, []servicehost.Hook{
			{Name: "drain_scheduler", Fn: func(hookCtx context.Context) error {
				if err := runtime.scheduler.Shutdown(hookCtx); err != nil {
					return err
				}
				cancelBackground()
				done := make(chan struct{})
				go func() { background.Wait(); close(done) }()
				select {
				case <-done:
					return nil
				case <-hookCtx.Done():
					return hookCtx.Err()
				}
			}},
			{Name: "revoke_capabilities", Fn: func(hookCtx context.Context) error {
				return revokeRuntimeCapabilities(hookCtx, runtime)
			}},
			{Name: "stop_worker_agents_sessions", Fn: func(hookCtx context.Context) error {
				var first error
				if shutdowner, ok := runtime.slotReconciler.(interface{ Shutdown(context.Context) error }); ok {
					first = shutdowner.Shutdown(hookCtx)
				}
				if runtime.automation != nil {
					first = errors.Join(first, runtime.automation.Close(hookCtx))
				}
				return first
			}},
			{Name: "flush_checkpoint", Fn: func(hookCtx context.Context) error {
				if runtime.diagnostics != nil {
					if err := runtime.diagnostics.Flush(hookCtx); err != nil {
						return err
					}
				}
				_, err := runtime.store.OperationalSnapshot(time.Now().UTC())
				return err
			}},
		})
		if result.Forced && runErr == nil {
			runErr = servicehost.ErrForcedShutdown
		}
		if result.Forced {
			runtime.record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "forced", ErrorClass: "forced_shutdown"})
		}
	}()
	runtime.refreshMetrics(time.Now().UTC())
	runtime.record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "startup", Outcome: "ready", Resource: options.backend})
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
			runtime.record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "scheduler", Outcome: "completed", RequestID: outcome.Request.RequestID})
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

func runSlotSessionController(ctx context.Context, controller slotOperationReconciler, owner string, interval time.Duration, record func(error)) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	runPass := func() {
		if err := controller.ReconcileSlotSessionOperations(ctx, owner, time.Now().UTC()); err != nil && record != nil {
			record(err)
		}
	}
	runPass()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			runPass()
		}
	}
}
