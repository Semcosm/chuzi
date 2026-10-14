package slotwindows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const sessionBrokerJournalVersion = 1
const sessionBrokerJournalMaxBytes = 256 * 1024

// FileSessionBrokerJournal is the deployment journal used by the broker
// helper. Its path must come from the installation, never from a broker pipe
// request or operator input. The file contains only ownership facts.
type FileSessionBrokerJournal struct {
	path string
}

func NewFileSessionBrokerJournal(path string) (*FileSessionBrokerJournal, error) {
	if path == "" || filepath.Clean(path) != path || !filepath.IsAbs(path) || path == string(filepath.VolumeName(path))+string(filepath.Separator) {
		return nil, ErrSessionBrokerInvalidRequest
	}
	if err := validateSessionBrokerJournalPath(path); err != nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	return &FileSessionBrokerJournal{path: path}, nil
}

func (j *FileSessionBrokerJournal) Load(ctx context.Context) (SessionBrokerJournalState, error) {
	if j == nil || ctx == nil {
		return SessionBrokerJournalState{}, ErrSessionBrokerInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return SessionBrokerJournalState{}, err
	}
	if err := validateSessionBrokerJournalPath(j.path); err != nil {
		return SessionBrokerJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return SessionBrokerJournalState{Version: sessionBrokerJournalVersion}, nil
	}
	if err != nil || len(data) > sessionBrokerJournalMaxBytes {
		return SessionBrokerJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state SessionBrokerJournalState
	if err := decoder.Decode(&state); err != nil || state.Version != sessionBrokerJournalVersion {
		return SessionBrokerJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SessionBrokerJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	return state, nil
}

func (j *FileSessionBrokerJournal) Save(ctx context.Context, state SessionBrokerJournalState) error {
	if j == nil || ctx == nil || state.Version != sessionBrokerJournalVersion {
		return ErrSessionBrokerInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionBrokerJournalPath(j.path); err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > sessionBrokerJournalMaxBytes {
		return ErrSessionBrokerInvalidRequest
	}
	tmp, err := os.CreateTemp(filepath.Dir(j.path), ".session-broker-ownership-*")
	if err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return ErrSessionBrokerUnknownOwnership
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return ErrSessionBrokerUnknownOwnership
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return ErrSessionBrokerUnknownOwnership
	}
	if err := tmp.Close(); err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	if err := replaceSessionBrokerJournal(tmpName, j.path); err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	return nil
}
