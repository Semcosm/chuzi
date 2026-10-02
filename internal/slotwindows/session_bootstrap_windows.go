//go:build windows

package slotwindows

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// runnerSessionBootstrapPipe is fixed at build time. It is not configurable
// by Core, UI, Matrix, a request, or the smoke script. The runner-owned
// helper must ACL this pipe to the service and its own broker identity.
const runnerSessionBootstrapPipe = `\\.\pipe\chuzi-session-bootstrap-v1`

type runnerSessionBootstrapper struct{}

// NewRunnerSessionBootstrapper returns the fixed Windows runner boundary used
// by the opt-in native smoke. Production configuration leaves this unset.
func NewRunnerSessionBootstrapper() SessionBootstrapper {
	return runnerSessionBootstrapper{}
}

type runnerSessionRequest struct {
	Version    int    `json:"version"`
	Operation  string `json:"operation"`
	SlotID     string `json:"slot_id"`
	Ordinal    int    `json:"ordinal"`
	Generation uint64 `json:"generation"`
	SID        string `json:"sid"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	SessionID  uint32 `json:"session_id,omitempty"`
}

type runnerSessionResponse struct {
	ID    uint32 `json:"id"`
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
}

func (runnerSessionBootstrapper) Start(ctx context.Context, identity ManagedIdentity) (BootstrapSession, error) {
	return callRunnerSessionProvider(ctx, runnerSessionRequest{
		Version: 1, Operation: "start", SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID,
	})
}

func (runnerSessionBootstrapper) StartWithCredential(ctx context.Context, identity ManagedIdentity, username string, password []uint16) (BootstrapSession, error) {
	if strings.TrimSpace(username) == "" || len(password) < 2 {
		return BootstrapSession{}, ErrSessionIdentity
	}
	request := runnerSessionRequest{
		Version: 1, Operation: "start", SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, Username: username, Password: windows.UTF16ToString(password[:len(password)-1]),
	}
	defer func() {
		request.Password = ""
	}()
	return callRunnerSessionProvider(ctx, request)
}

func (runnerSessionBootstrapper) Stop(ctx context.Context, identity ManagedIdentity, session BootstrapSession) error {
	if session.ID == 0 {
		return nil
	}
	_, err := callRunnerSessionProvider(ctx, runnerSessionRequest{
		Version: 1, Operation: "stop", SlotID: identity.SlotID, Ordinal: identity.Ordinal, Generation: identity.Generation, SID: identity.SID, SessionID: session.ID,
	})
	return err
}

func callRunnerSessionProvider(ctx context.Context, request runnerSessionRequest) (BootstrapSession, error) {
	if ctx == nil || ctx.Err() != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return BootstrapSession{}, ErrSessionUnavailable
	}
	payload = append(payload, '\n')
	result := make(chan BootstrapSession, 1)
	go func() {
		defer clear(payload)
		file, openErr := os.OpenFile(runnerSessionBootstrapPipe, os.O_RDWR, 0)
		if openErr != nil {
			result <- BootstrapSession{}
			return
		}
		defer file.Close()
		if _, writeErr := file.Write(payload); writeErr != nil {
			result <- BootstrapSession{}
			return
		}
		reader := bufio.NewReader(io.LimitReader(file, 8192))
		var response runnerSessionResponse
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		if decodeErr := decoder.Decode(&response); decodeErr != nil || response.ID == 0 || response.State != "active" {
			result <- BootstrapSession{}
			return
		}
		result <- BootstrapSession{ID: response.ID, State: response.State}
	}()
	select {
	case <-ctx.Done():
		return BootstrapSession{}, ErrSessionUnavailable
	case session := <-result:
		if session.ID == 0 {
			return BootstrapSession{}, ErrSessionBootstrapUnavailable
		}
		return session, nil
	}
}
