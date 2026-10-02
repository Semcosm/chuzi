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
)

// run owns the long-lived service loop. Runtime assembly and maintenance
// commands live in separate modules so this supervisor only coordinates
// lifecycle, endpoints, and scheduling.
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
		if runtime.rdp != nil {
			_ = runtime.rdp.Close()
		}
		if runtime.coreServer != nil {
			_ = runtime.coreServer.Close()
		}
		if runtime.automation != nil {
			_ = runtime.automation.Close(context.Background())
		}
		if closeErr := runtime.store.Close(); closeErr != nil && runtime.logger != nil {
			runtime.logger.Record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "failed", ErrorClass: "store_close_failed"})
		}
		if runtime.logger != nil {
			runtime.logger.Record(observability.Event{At: time.Now().UTC(), Component: "service", Operation: "shutdown", Outcome: "stopping"})
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
	if runtime.slotReconciler != nil || runtime.jobPoolID != "" {
		startBackground("slot lifecycle", func(workerCtx context.Context) error {
			reconcile := func() {
				var err error
				environmentID, environmentVersion := runtime.environmentID, runtime.environmentVersion
				if runtime.jobPoolID != "" {
					if current, getErr := runtime.store.GetJobPool(runtime.jobPoolID); getErr == nil {
						environmentID, environmentVersion = current.EnvironmentID, current.EnvironmentVersion
					}
					if _, controlErr := runtime.store.ReconcileJobPoolControl(runtime.jobPoolID, time.Now().UTC()); controlErr != nil {
						err = controlErr
					}
				}
				if runtime.environment != nil {
					// Revalidate the signed tree before each slot pass. Store remains
					// the projection used by claims and capacity, so an external
					// package edit immediately removes readiness from both views.
					if _, healthErr := runtime.environment.HealthCheck(workerCtx, environmentID, environmentVersion); healthErr != nil {
						err = healthErr
					}
					if syncErr := runtime.environment.SyncRecords(runtime.store); err == nil && syncErr != nil {
						err = syncErr
					}
				}
				if err == nil && runtime.slotReconciler != nil {
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
		cancelBackground()
		background.Wait()
		if shutdowner, ok := runtime.slotReconciler.(interface{ Shutdown(context.Context) error }); ok {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = shutdowner.Shutdown(shutdownCtx)
			shutdownCancel()
		}
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
