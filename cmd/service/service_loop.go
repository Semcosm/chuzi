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
