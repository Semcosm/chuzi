package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validIdentity() Identity {
	return Identity{PoolID: "pool-001", SlotID: "slot-001", Ordinal: 1, Generation: 3, SID: "S-1-5-21-100-200-300-400", Owner: "service-owner"}
}

func TestIdentityAndSessionFence(t *testing.T) {
	identity := validIdentity()
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	session := Session{ID: 7, SID: identity.SID, State: "active", ObservedAt: now}
	if err := session.ValidateActive(identity); err != nil {
		t.Fatal(err)
	}
	session.SID = "S-1-5-21-9"
	if !errors.Is(session.ValidateActive(identity), ErrSessionChanged) {
		t.Fatal("SID mismatch was accepted")
	}
}

func TestStableErrorRetryPolicy(t *testing.T) {
	if !NewError(CodeSessionUnavailable, "redacted").Retryable() || !NewError(CodeDeadline, "redacted").Retryable() {
		t.Fatal("transient session errors were not retryable")
	}
	if NewError(CodeOwnershipUnknown, "redacted").Retryable() || NewError(CodeSessionChanged, "redacted").Retryable() {
		t.Fatal("ownership or identity errors were incorrectly retryable")
	}
}

func TestSecretLeaseNeverSerializesSecret(t *testing.T) {
	lease, err := NewSecretLease("ChuziJob0001", []uint16{'s', 'e', 'c', 0})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" || strings.Contains(string(data), "ChuziJob") || strings.Contains(string(data), "sec") {
		t.Fatalf("secret lease serialized: %s", data)
	}
	var seen []uint16
	if err := lease.WithPassword(func(value []uint16) error {
		seen = append(seen, value...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string([]byte{byte(seen[0]), byte(seen[1]), byte(seen[2])}) != "sec" {
		t.Fatal("temporary password view was not delivered")
	}
	lease.Wipe()
	if lease.Username() != "" || lease.WithPassword(func([]uint16) error { return nil }) == nil {
		t.Fatal("wiped lease remained usable")
	}
}

func TestWireAndCapabilitiesFailClosed(t *testing.T) {
	if err := ValidateWire([]byte("{\"version\":1,\"operation\":\"start\"}")); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"{\"password\":\"x\"}", "{\"profile_path\":\"x\"}", "{\"endpoint\":\"x\"}", "{\"meta\":{\"credential_blob\":\"x\"}}", "null", "[]"} {
		if !errors.Is(ValidateWire([]byte(data)), ErrInvalidArgument) {
			t.Fatalf("sensitive field accepted: %s", data)
		}
	}
	if _, err := (DenyAuthorizer{}).Authorize(context.Background(), CapabilityRequest{Kind: CapabilityWorkspace}); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("deny authorizer returned %v", err)
	}
}

func TestReadinessFreshnessAndOwnershipValidation(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	check := ReadinessCheck{Layer: LayerWTS, State: ReadinessReady, ObservedAt: now, FreshUntil: now.Add(time.Second)}
	if !check.Fresh(now) || check.Fresh(now.Add(time.Second)) || check.Fresh(now.Add(-time.Second)) {
		t.Fatal("freshness boundary is not strict")
	}
	record := OwnershipRecord{Version: OwnershipJournalVersion, PoolID: "pool-001", SlotID: "slot-001", Ordinal: 1, SID: validIdentity().SID, Generation: 3, Owner: "service-owner", Phase: JournalIntent, Revision: 1, UpdatedAt: now}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	record.Phase = JournalPhase("garbage")
	if !errors.Is(record.Validate(), ErrInvalidArgument) {
		t.Fatal("unknown journal phase accepted")
	}
	host := HostOwnerRecord{Version: OwnershipJournalVersion, OwnerID: "install-1", ServiceSID: "S-1-5-18", BrokerSID: "S-1-5-21-9", DatabaseHash: "sha256:db", PipeName: BrokerPipeName, LeaseID: "lease-1", ExpiresAt: now.Add(time.Minute), UpdatedAt: now}
	if err := host.Validate(); err != nil {
		t.Fatal(err)
	}
	host.PipeName = "\\\\.\\pipe\\other"
	if !errors.Is(host.Validate(), ErrInvalidArgument) {
		t.Fatal("non-fixed host pipe accepted")
	}
}
