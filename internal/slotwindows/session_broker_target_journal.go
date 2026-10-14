package slotwindows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Semcosm/chuzi/internal/sessionruntime"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const brokerTargetJournalVersion = 1
const brokerTargetJournalMaxBytes = 1024 * 1024

// FileBrokerTargetJournal is the deployment journal used by the broker
// helper. Its path must come from the installation, never from a broker pipe
// request or operator input. The file contains only ownership facts.
type FileBrokerTargetJournal struct {
	path string
	mu   sync.Mutex
}

func NewFileBrokerTargetJournal(path string) (*FileBrokerTargetJournal, error) {
	if path == "" || filepath.Clean(path) != path || !filepath.IsAbs(path) || path == string(filepath.VolumeName(path))+string(filepath.Separator) {
		return nil, ErrSessionBrokerInvalidRequest
	}
	if err := validateSessionBrokerJournalPath(path); err != nil {
		return nil, ErrSessionBrokerInvalidRequest
	}
	return &FileBrokerTargetJournal{path: path}, nil
}

func (j *FileBrokerTargetJournal) load(ctx context.Context) (brokerTargetJournalState, error) {
	if j == nil || ctx == nil {
		return brokerTargetJournalState{}, ErrSessionBrokerInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return brokerTargetJournalState{}, err
	}
	if err := validateSessionBrokerJournalPath(j.path); err != nil {
		return brokerTargetJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return brokerTargetJournalState{Version: brokerTargetJournalVersion}, nil
	}
	if err != nil || len(data) > brokerTargetJournalMaxBytes {
		return brokerTargetJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state brokerTargetJournalState
	if err := decoder.Decode(&state); err != nil || state.Version != brokerTargetJournalVersion {
		return brokerTargetJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return brokerTargetJournalState{}, ErrSessionBrokerUnknownOwnership
	}
	return state, nil
}

func (j *FileBrokerTargetJournal) save(ctx context.Context, state brokerTargetJournalState) error {
	if j == nil || ctx == nil || state.Version != brokerTargetJournalVersion {
		return ErrSessionBrokerInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionBrokerJournalPath(j.path); err != nil {
		return ErrSessionBrokerUnknownOwnership
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > brokerTargetJournalMaxBytes {
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

type brokerTargetJournalState struct {
	Version      int                                `json:"version"`
	Reservations []sessionruntime.TargetReservation `json:"reservations,omitempty"`
}

func (j *FileBrokerTargetJournal) List(ctx context.Context) ([]sessionruntime.TargetReservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.load(ctx)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]bool)
	slots := make(map[string]bool)
	for _, r := range state.Reservations {
		if r.Validate() != nil || !managedSIDPattern.MatchString(r.SID) || r.Ordinal < 1 || r.Ordinal > 256 || targets[r.TargetID] || slots[r.SlotID] {
			return nil, ErrSessionBrokerUnknownOwnership
		}
		targets[r.TargetID] = true
		slots[r.SlotID] = true
	}
	return state.Reservations, nil
}

func (j *FileBrokerTargetJournal) Claim(ctx context.Context, reservation sessionruntime.TargetReservation) error {
	if reservation.Validate() != nil || !managedSIDPattern.MatchString(reservation.SID) || reservation.Ordinal < 1 || reservation.Ordinal > 256 {
		return sessionruntime.ErrTargetOwnership
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.load(ctx)
	if err != nil {
		return err
	}
	for _, current := range state.Reservations {
		if current.TargetID == reservation.TargetID || current.SlotID == reservation.SlotID {
			return sessionruntime.ErrTargetCollision
		}
	}
	state.Reservations = append(state.Reservations, reservation)
	return j.save(ctx, state)
}

func (j *FileBrokerTargetJournal) Release(ctx context.Context, reservation sessionruntime.TargetReservation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.load(ctx)
	if err != nil {
		return err
	}
	for i, current := range state.Reservations {
		if current.TargetID != reservation.TargetID {
			continue
		}
		if current != reservation {
			return sessionruntime.ErrTargetOwnership
		}
		state.Reservations = append(state.Reservations[:i], state.Reservations[i+1:]...)
		return j.save(ctx, state)
	}
	return sessionruntime.ErrTargetOwnership
}
