package slotwindows

import (
	"context"
	"errors"
	"testing"
)

func TestManagedIdentityValidationRejectsUntrustedValues(t *testing.T) {
	valid := ManagedIdentity{SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1"}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid managed identity rejected: %v", err)
	}
	for _, value := range []ManagedIdentity{
		{},
		{SlotID: "../escape", Ordinal: 1, Generation: 1, SID: valid.SID},
		{SlotID: valid.SlotID, Ordinal: 0, Generation: 1, SID: valid.SID},
		{SlotID: valid.SlotID, Ordinal: 1, Generation: 0, SID: valid.SID},
		{SlotID: valid.SlotID, Ordinal: 1, Generation: 1},
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
	_, err := startManagedSession(context.Background(), provider, ManagedIdentity{SlotID: "slot-001", Ordinal: 1, Generation: 1, SID: "S-1-5-21-1"}, "derived-user", []uint16{'s', 'e', 'c', 0})
	if !errors.Is(err, ErrSessionBootstrapUnavailable) {
		t.Fatalf("unexpected provider error: %v", err)
	}
}
