package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Semcosm/chuzi/internal/config"
	"github.com/Semcosm/chuzi/internal/coretransport"
)

// endpointRunner is the endpoint-specific part of service supervision. The
// supervisor owns goroutine/error policy; this module owns listener creation
// and endpoint shutdown behavior.
type endpointRunner struct {
	name string
	run  func(context.Context) error
}

func wireEndpointRunners(ctx context.Context, runtime *serviceRuntime, cfg config.Config) ([]endpointRunner, error) {
	if ctx == nil || runtime == nil || runtime.coreAPI == nil {
		return nil, errInvalidOptions
	}
	listener, err := coretransport.Listen(ctx, coretransport.EndpointPath(cfg.DataDir))
	if err != nil {
		return nil, err
	}
	server, err := coretransport.NewServer(runtime.coreAPI, listener, coretransport.Config{})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	runtime.coreServer = server
	runners := []endpointRunner{{
		name: "core api",
		run: func(workerCtx context.Context) error {
			go func() {
				<-workerCtx.Done()
				_ = server.Close()
			}()
			return server.Serve()
		},
	}}
	if runtime.health != nil && strings.TrimSpace(runtime.healthListen) != "" {
		server := &http.Server{Addr: runtime.healthListen, Handler: runtime.health.Handler(), ReadHeaderTimeout: 5 * time.Second}
		runners = append(runners, httpEndpointRunner("health endpoint", server))
	}
	if runtime.metrics != nil && strings.TrimSpace(runtime.metricsListen) != "" {
		server := &http.Server{Addr: runtime.metricsListen, Handler: runtime.metrics.Handler(), ReadHeaderTimeout: 5 * time.Second}
		runners = append(runners, httpEndpointRunner("metrics endpoint", server))
	}
	return runners, nil
}

func httpEndpointRunner(name string, server *http.Server) endpointRunner {
	return endpointRunner{
		name: name,
		run: func(workerCtx context.Context) error {
			go func() {
				<-workerCtx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx)
			}()
			err := server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
}
