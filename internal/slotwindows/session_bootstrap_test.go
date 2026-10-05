package slotwindows

import (
	"context"
	"errors"
	"testing"
)

func TestManagedIdentityValidationRejectsUntrustedValues(t *testing.T) {
	valid := ManagedIdentity{SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1", Owner: "owner-1"}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid managed identity rejected: %v", err)
	}
	for _, value := range []ManagedIdentity{
		{},
		{SlotID: "../escape", Ordinal: 1, Generation: 1, SID: valid.SID},
		{SlotID: valid.SlotID, Ordinal: 0, Generation: 1, SID: valid.SID},
		{SlotID: valid.SlotID, Ordinal: 1, Generation: 0, SID: valid.SID},
		{SlotID: valid.SlotID, Ordinal: 1, Generation: 1, SID: valid.SID},
	} {
		if err := value.validate(); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("invalid managed identity was accepted: %+v", value)
		}
	}
}

type noCredentialBootstrapper struct{}

func (noCredentialBootstrapper) Start(context.Context, ManagedIdentity) (BootstrapSession, error) {
	return BootstrapSession{}, ErrSessionBootstrapUnavailable
}

func (noCredentialBootstrapper) Stop(context.Context, ManagedIdentity, BootstrapSession) error {
	return nil
}

func TestStartManagedSessionDoesNotExposePasswordToPublicProvider(t *testing.T) {
	provider := noCredentialBootstrapper{}
	_, err := startManagedSession(context.Background(), provider, ManagedIdentity{SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1", Owner: "owner-1"}, "derived-user", []uint16{'s', 'e', 'c', 0})
	if !errors.Is(err, ErrSessionBootstrapUnavailable) {
		t.Fatalf("unexpected provider error: %v", err)
	}
}

type recordingLoginAdapter struct {
	received []uint16
}

func (a *recordingLoginAdapter) Start(_ context.Context, _ ManagedIdentity, _ string, password []uint16) (BootstrapSession, error) {
	a.received = password
	return BootstrapSession{ID: 44, State: "active"}, nil
}

func (a *recordingLoginAdapter) Stop(context.Context, ManagedIdentity, BootstrapSession) error {
	return nil
}

func TestSessionLoginAdapterPasswordBufferIsClearedAfterCall(t *testing.T) {
	var adapter recordingLoginAdapter
	password := []uint16{'s', 'e', 'c', 0}
	_, err := startManagedLoginSession(context.Background(), &adapter, ManagedIdentity{SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1", Owner: "owner-1"}, "derived-user", password)
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != 4 || password[0] != 's' || password[1] != 'e' || password[2] != 'c' || password[3] != 0 {
		t.Fatal("caller-owned credential was unexpectedly modified")
	}
	if len(adapter.received) == 0 || adapter.received[0] != 0 || adapter.received[1] != 0 || adapter.received[2] != 0 || adapter.received[3] != 0 {
		t.Fatal("adapter credential buffer was not cleared")
	}
}
