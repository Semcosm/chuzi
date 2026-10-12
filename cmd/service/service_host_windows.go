//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/servicehost"
	"golang.org/x/sys/windows/svc"
)

type windowsServiceHost struct {
	options serviceOptions
}

func (h windowsServiceHost) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 5000}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, h.options) }()
	statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			statuses <- svc.Status{State: svc.Stopped}
			return false, 0
		case request := <-requests:
			if request.Cmd != svc.Stop && request.Cmd != svc.Shutdown {
				continue
			}
			statuses <- svc.Status{State: svc.StopPending, WaitHint: uint32(h.options.shutdownTimeout / time.Millisecond)}
			cancel()
			limit := h.options.shutdownTimeout
			if limit <= 0 {
				limit = 5 * time.Second
			}
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
					return true, 1
				}
				statuses <- svc.Status{State: svc.Stopped}
				return false, 0
			case <-time.After(limit):
				// Returning makes SCM own the bounded forced termination/recovery
				// path; no unrelated process is searched or killed here.
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return true, 1
			}
		}
	}
}

func runServiceHost(ctx context.Context, options serviceOptions) error {
	if err := validateFixedServiceEntrypoint(options.configPath); err != nil {
		return err
	}
	if options.configPath == "" {
		return servicehost.ErrInvalidConfig
	}
	if err := servicehost.ValidateServiceToken(servicehost.ProductionIdentity()); err != nil {
		return err
	}
	// SCM supplies only the fixed service/config arguments. Derive every
	// executable and script used by the LocalSystem process from that signed
	// installation root so a relative PATH or working directory cannot select
	// a user-writable worker.
	if err := pinServiceRuntime(&options); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executable, err := os.Executable()
	if err != nil {
		return servicehost.ErrHostUnavailable
	}
	runtime, err := servicehost.NewTrustedRuntime(filepath.Dir(filepath.Clean(executable)))
	if err != nil {
		return err
	}
	dataRoot, err := servicehost.FixedDataRoot()
	if err != nil {
		return err
	}
	if _, err := servicehost.RunWindowsPreflight(ctx, servicehost.WindowsPreflightOptions{
		Runtime: runtime, DataRoot: dataRoot, MaxSessions: 32,
	}, time.Now); err != nil {
		return err
	}
	return svc.Run(servicehost.ServiceName, windowsServiceHost{options: options})
}

func pinServiceRuntime(options *serviceOptions) error {
	if options == nil {
		return servicehost.ErrInvalidConfig
	}
	executable, err := os.Executable()
	if err != nil {
		return servicehost.ErrInvalidConfig
	}
	runtimeRoot := filepath.Dir(filepath.Clean(executable))
	trusted, err := servicehost.NewTrustedRuntime(runtimeRoot)
	if err != nil {
		return err
	}
	options.workerCommand = trusted.Node
	workerName := "worker.mjs"
	if options.backend == backendHeadless || options.backend == backendHeaded {
		workerName = "headless.mjs"
	}
	options.workerScript = filepath.Join(runtimeRoot, "browser-worker", "src", workerName)
	if filepath.Clean(options.workerScript) != options.workerScript || !withinServiceRoot(runtimeRoot, options.workerScript) {
		return servicehost.ErrInvalidConfig
	}
	return nil
}

func withinServiceRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateFixedServiceEntrypoint(configPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return servicehost.ErrInvalidConfig
	}
	if _, err := servicehost.NewTrustedRuntime(filepath.Dir(filepath.Clean(executable))); err != nil {
		return err
	}
	dataRoot, err := servicehost.FixedDataRoot()
	if err != nil {
		return err
	}
	want := filepath.Clean(filepath.Join(dataRoot, "core-config.json"))
	raw := strings.TrimSpace(configPath)
	got := filepath.Clean(raw)
	if raw == "" || raw != got || !strings.EqualFold(got, want) {
		return servicehost.ErrInvalidConfig
	}
	return nil
}
