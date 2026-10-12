//go:build windows

package slotwindows

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Microsoft/go-winio"
	"github.com/Semcosm/chuzi/internal/coretransport"
)

// runnerSessionBootstrapPipe is fixed at build time. It is not configurable
// by Core, UI, Matrix, a request, or the smoke script. The runner-owned
// helper must ACL this pipe to the service and its own broker identity.
const runnerSessionBootstrapPipe = SessionBrokerPipeName
const runnerSessionBrokerExecutableName = "chuzi-session-broker.exe"

type runnerSessionBootstrapper struct {
	expectedServerExecutable string
}

// NewRunnerSessionBootstrapper returns the fixed Windows broker boundary used
// by service assembly and the opt-in native smoke. The endpoint is fixed at
// build time and carries no credentials.
func NewRunnerSessionBootstrapper() SessionBootstrapper {
	executable, err := os.Executable()
	if err != nil || filepath.Clean(executable) != executable || !filepath.IsAbs(executable) {
		return runnerSessionBootstrapper{}
	}
	return runnerSessionBootstrapper{expectedServerExecutable: filepath.Join(filepath.Dir(executable), runnerSessionBrokerExecutableName)}
}

func (b runnerSessionBootstrapper) Start(ctx context.Context, identity ManagedIdentity) (BootstrapSession, error) {
	return callRunnerSessionProviderWithExecutable(ctx, SessionBrokerRequest{
		Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner,
	}, b.expectedServerExecutable)
}

func (b runnerSessionBootstrapper) Stop(ctx context.Context, identity ManagedIdentity, session BootstrapSession) error {
	if session.ID == 0 {
		return nil
	}
	_, err := callRunnerSessionProviderWithExecutable(ctx, SessionBrokerRequest{
		Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStop, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner, SessionID: session.ID,
	}, b.expectedServerExecutable)
	return err
}

// callRunnerSessionProvider is retained for deterministic transport tests and
// older in-package callers; production assembly uses the executable-bound
// variant above.
func callRunnerSessionProvider(ctx context.Context, request SessionBrokerRequest) (BootstrapSession, error) {
	executable, err := os.Executable()
	if err != nil || filepath.Clean(executable) != executable || !filepath.IsAbs(executable) {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	return callRunnerSessionProviderWithExecutable(ctx, request, filepath.Join(filepath.Dir(executable), runnerSessionBrokerExecutableName))
}

func callRunnerSessionProviderWithExecutable(ctx context.Context, request SessionBrokerRequest, expectedExecutable string) (BootstrapSession, error) {
	if ctx == nil || ctx.Err() != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	if err := request.validate(); err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, sessionBrokerCallTimeout)
	defer cancel()
	conn, err := winio.DialPipeContext(callCtx, runnerSessionBootstrapPipe)
	if err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	defer conn.Close()
	if expectedExecutable == "" || coretransport.ValidateWindowsPipeExecutable(conn, expectedExecutable) != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	return exchangeSessionBroker(callCtx, conn, request)
}
