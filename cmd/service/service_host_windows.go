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
	options  serviceOptions
	prepare  func(context.Context, serviceOptions) (serviceOptions, error)
	run      func(context.Context, serviceOptions) error
	diagnose func(serviceHostDiagnosticEvent)
}

func (h windowsServiceHost) recordDiagnostic(event serviceHostDiagnosticEvent) {
	if h.diagnose != nil {
		h.diagnose(event)
	}
}

func (h windowsServiceHost) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 5000}
	h.recordDiagnostic(serviceHostDiagnosticEvent{Stage: "service_dispatch", Outcome: "entered"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepare := h.prepare
	if prepare == nil {
		prepare = prepareWindowsService
	}
	runner := h.run
	if runner == nil {
		runner = run
	}
	type serviceEvent struct {
		ready bool
		err   error
	}
	events := make(chan serviceEvent, 2)
	go func() {
		options, err := prepare(ctx, h.options)
		if err != nil {
			h.recordDiagnostic(serviceHostDiagnosticEvent{
				Stage: serviceStartupStage(err, "service_preparation"), Outcome: "failed", ErrorClass: serviceHostErrorClass(err),
			})
			events <- serviceEvent{err: err}
			return
		}
		if err := ctx.Err(); err != nil {
			h.recordDiagnostic(serviceHostDiagnosticEvent{
				Stage: "service_preparation", Outcome: "cancelled", ErrorClass: serviceHostErrorClass(err),
			})
			events <- serviceEvent{err: err}
			return
		}
		h.recordDiagnostic(serviceHostDiagnosticEvent{Stage: "service_preparation", Outcome: "completed"})
		events <- serviceEvent{ready: true}
		runErr := runner(ctx, options)
		event := serviceHostDiagnosticEvent{Stage: serviceStartupStage(runErr, "service_runtime")}
		switch {
		case runErr != nil:
			event.Outcome = "failed"
			event.ErrorClass = serviceHostErrorClass(runErr)
		case ctx.Err() != nil:
			event.Outcome = "stopped"
		default:
			event.Outcome = "returned"
		}
		h.recordDiagnostic(event)
		events <- serviceEvent{err: runErr}
	}()
	starting := true
	stopping := false
	checkpoint := uint32(1)
	startTicker := time.NewTicker(2 * time.Second)
	defer startTicker.Stop()
	var stopTimer <-chan time.Time
	for {
		select {
		case event := <-events:
			if event.ready {
				starting = false
				if !stopping {
					statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
					h.recordDiagnostic(serviceHostDiagnosticEvent{Stage: "service_state", Outcome: "running"})
				}
				continue
			}
			if event.err != nil && !errors.Is(event.err, context.Canceled) {
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return true, 1
			}
			statuses <- svc.Status{State: svc.Stopped}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if request.Cmd == svc.Interrogate {
				if stopping {
					statuses <- svc.Status{State: svc.StopPending, WaitHint: shutdownWaitHint(h.options.shutdownTimeout)}
				} else if starting {
					statuses <- svc.Status{State: svc.StartPending, WaitHint: 5000}
				} else {
					statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
				}
				continue
			}
			if request.Cmd != svc.Stop && request.Cmd != svc.Shutdown {
				continue
			}
			if stopping {
				continue
			}
			stopping = true
			control := "stop"
			if request.Cmd == svc.Shutdown {
				control = "shutdown"
			}
			statuses <- svc.Status{State: svc.StopPending, WaitHint: shutdownWaitHint(h.options.shutdownTimeout)}
			h.recordDiagnostic(serviceHostDiagnosticEvent{Stage: "service_control", Outcome: "received", Control: control})
			cancel()
			limit := h.options.shutdownTimeout
			if limit <= 0 {
				limit = 5 * time.Second
			}
			stopTimer = time.After(limit)
		case <-startTicker.C:
			if starting && !stopping {
				checkpoint++
				statuses <- svc.Status{State: svc.StartPending, CheckPoint: checkpoint, WaitHint: 5000}
			}
		case <-stopTimer:
			// Returning makes SCM own the bounded forced termination/recovery
			// path; no unrelated process is searched or killed here.
			statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
			return true, 1
		}
	}
}

func shutdownWaitHint(timeout time.Duration) uint32 {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	milliseconds := timeout / time.Millisecond
	if milliseconds > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(milliseconds)
}

func runServiceHost(ctx context.Context, options serviceOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Register with SCM before filesystem, token, or host probes. SCM allows
	// only a short connection window after process creation.
	err := svc.Run(servicehost.ServiceName, windowsServiceHost{options: options, diagnose: recordServiceHostDiagnostic})
	if err != nil {
		recordServiceHostDiagnostic(serviceHostDiagnosticEvent{
			Stage: "service_dispatch", Outcome: "failed", ErrorClass: serviceHostErrorClass(err),
		})
	}
	return err
}

func prepareWindowsService(ctx context.Context, options serviceOptions) (serviceOptions, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return serviceOptions{}, err
	}
	if err := validateFixedServiceEntrypoint(options.configPath); err != nil {
		return serviceOptions{}, withServiceStartupStage("entrypoint_validation", err)
	}
	if options.configPath == "" {
		return serviceOptions{}, withServiceStartupStage("configuration_path", servicehost.ErrInvalidConfig)
	}
	if err := servicehost.ValidateServiceToken(servicehost.ProductionIdentity()); err != nil {
		return serviceOptions{}, withServiceStartupStage("service_identity", err)
	}
	if err := servicehost.EnableServicePrivileges(servicehost.ProductionIdentity()); err != nil {
		return serviceOptions{}, withServiceStartupStage("service_privileges", err)
	}
	// SCM supplies only the fixed service/config arguments. Derive every
	// executable and script used by the LocalSystem process from that signed
	// installation root so a relative PATH or working directory cannot select
	// a user-writable worker.
	if err := pinServiceRuntime(&options); err != nil {
		return serviceOptions{}, withServiceStartupStage("runtime_pin", err)
	}
	if err := ctx.Err(); err != nil {
		return serviceOptions{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return serviceOptions{}, withServiceStartupStage("runtime_executable", servicehost.ErrHostUnavailable)
	}
	runtime, err := servicehost.NewTrustedRuntime(filepath.Dir(filepath.Clean(executable)))
	if err != nil {
		return serviceOptions{}, withServiceStartupStage("runtime_layout", err)
	}
	requireAuthenticode, err := servicehost.RuntimeSignaturePolicy(runtime.Root)
	if err != nil {
		return serviceOptions{}, withServiceStartupStage("signature_policy", err)
	}
	if err := verifyWindowsServiceRuntime(runtime, requireAuthenticode); err != nil {
		return serviceOptions{}, withServiceStartupStage("runtime_integrity", err)
	}
	return options, nil
}

func verifyWindowsServiceRuntime(runtime servicehost.TrustedRuntime, requireAuthenticode bool) error {
	return servicehost.VerifyRuntimeSignature(runtime, requireAuthenticode)
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
