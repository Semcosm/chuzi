//go:build windows

package slotwindows

import (
	"bufio"
	"context"
	"io"
	"os"
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
	payload, err := EncodeSessionBrokerRequest(request)
	if err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	payload = append(payload, '\n')
	type callResult struct {
		session BootstrapSession
		err     error
	}
	result := make(chan callResult, 1)
	go func() {
		defer clear(payload)
		file, openErr := os.OpenFile(runnerSessionBootstrapPipe, os.O_RDWR, 0)
		if openErr != nil {
			result <- callResult{err: ErrSessionUnavailable}
			return
		}
		defer file.Close()
		if _, writeErr := file.Write(payload); writeErr != nil {
			result <- callResult{err: ErrSessionUnavailable}
			return
		}
		reader := bufio.NewReader(io.LimitReader(file, 8192))
		data, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			result <- callResult{err: ErrSessionUnavailable}
			return
		}
		response, decodeErr := DecodeSessionBrokerResponse(data)
		if decodeErr != nil || response.Code != SessionBrokerOK {
			result <- callResult{err: ErrSessionUnavailable}
			return
		}
		if request.Operation == SessionBrokerStart && (response.SessionID == 0 || response.State != "active") {
			result <- callResult{err: ErrSessionUnavailable}
			return
		}
		result <- callResult{session: BootstrapSession{ID: response.SessionID, State: response.State}}
	}()
	select {
	case <-ctx.Done():
		return BootstrapSession{}, ErrSessionUnavailable
	case result := <-result:
		if result.err != nil {
			return BootstrapSession{}, result.err
		}
		return result.session, nil
	}
}
