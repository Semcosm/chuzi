//go:build windows

package slotwindows

import (
	"context"

	"github.com/Microsoft/go-winio"
)

// runnerSessionBootstrapPipe is fixed at build time. It is not configurable
// by Core, UI, Matrix, a request, or the smoke script. The runner-owned
// helper must ACL this pipe to the service and its own broker identity.
const runnerSessionBootstrapPipe = SessionBrokerPipeName

type runnerSessionBootstrapper struct{}

// NewRunnerSessionBootstrapper returns the fixed Windows broker boundary used
// by service assembly and the opt-in native smoke. The endpoint is fixed at
// build time and carries no credentials.
func NewRunnerSessionBootstrapper() SessionBootstrapper {
	return runnerSessionBootstrapper{}
}

func (runnerSessionBootstrapper) Start(ctx context.Context, identity ManagedIdentity) (BootstrapSession, error) {
	return callRunnerSessionProvider(ctx, SessionBrokerRequest{
		Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStart, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner,
	})
}

func (runnerSessionBootstrapper) Stop(ctx context.Context, identity ManagedIdentity, session BootstrapSession) error {
	if session.ID == 0 {
		return nil
	}
	_, err := callRunnerSessionProvider(ctx, SessionBrokerRequest{
		Version: SessionBrokerProtocolVersion, Operation: SessionBrokerStop, SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Owner: identity.Owner, SessionID: session.ID,
	})
	return err
}

func callRunnerSessionProvider(ctx context.Context, request SessionBrokerRequest) (BootstrapSession, error) {
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
	return exchangeSessionBroker(callCtx, conn, request)
}
