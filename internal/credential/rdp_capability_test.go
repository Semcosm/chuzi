package credential

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type rdpAuthorizerFake struct {
	material RDPMaterial
	err      error
}

func (f rdpAuthorizerFake) AuthorizeRDP(context.Context, RDPAuthorization) (RDPMaterial, error) {
	return cloneRDPMaterial(f.material), f.err
}

func TestRDPServiceIssueResolveExpiryAndRevoke(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	material, err := NewRDPMaterial("rdp.internal", 3389, "authorized-user", "secret-password", "EXAMPLE", false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRDPService(rdpAuthorizerFake{material: material})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.Issue(context.Background(), RDPAuthorization{AccountID: "account-1", RequestID: "request-1", Actor: "windows-ui"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if capability.Token == "" || !strings.HasPrefix(capability.ID, "rdp_") {
		t.Fatalf("invalid capability: %#v", capability)
	}
	resolved, err := service.Resolve(context.Background(), capability, "windows-ui", now.Add(30*time.Second))
	if err != nil || string(resolved.PasswordCopy()) != "secret-password" {
		t.Fatalf("resolve = %v, password = %q", err, resolved.PasswordCopy())
	}
	if err := service.Revoke(context.Background(), capability, "windows-ui"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(context.Background(), capability, "windows-ui", now.Add(31*time.Second)); !errors.Is(err, ErrRDPRevoked) {
		t.Fatalf("resolve after revoke = %v, want ErrRDPRevoked", err)
	}

	second, err := service.Issue(context.Background(), RDPAuthorization{AccountID: "account-1", RequestID: "request-2", Actor: "windows-ui"}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(context.Background(), second, "windows-ui", now.Add(time.Second)); !errors.Is(err, ErrRDPExpired) {
		t.Fatalf("resolve after expiry = %v, want ErrRDPExpired", err)
	}
}

func TestRDPServiceFailsClosedAndRedactsLogs(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	material, _ := NewRDPMaterial("10.0.0.8", 3389, "user", "password-secret", "", false)
	service, _ := NewRDPService(rdpAuthorizerFake{material: material, err: errors.New("authorization backend path=/secret")})
	if _, err := service.Issue(context.Background(), RDPAuthorization{AccountID: "a", RequestID: "r", Actor: "ui"}, now, time.Minute); !errors.Is(err, ErrRDPUnauthorized) {
		t.Fatalf("unauthorized issue = %v", err)
	}

	service, _ = NewRDPService(rdpAuthorizerFake{material: material})
	capability, err := service.Issue(context.Background(), RDPAuthorization{AccountID: "a", RequestID: "r", Actor: "ui"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(context.Background(), capability, "other-ui", now); !errors.Is(err, ErrRDPUnauthorized) {
		t.Fatalf("wrong actor resolve = %v", err)
	}
	encoded, err := json.Marshal(capability)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password-secret") || strings.Contains(capability.String(), capability.Token) {
		t.Fatalf("capability leaked secret or token: json=%s string=%s", encoded, capability.String())
	}
	redacted := RedactRDPCapability(capability)
	if _, ok := redacted["token"]; ok {
		t.Fatal("redacted capability contains token")
	}
}

func TestRDPServiceRejectsInvalidLeaseAndContext(t *testing.T) {
	material, _ := NewRDPMaterial("host", 3389, "user", "password", "", false)
	service, _ := NewRDPService(rdpAuthorizerFake{material: material})
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	request := RDPAuthorization{AccountID: "a", RequestID: "r", Actor: "ui"}
	if _, err := service.Issue(context.Background(), request, now, MaxRDPLease+time.Second); !errors.Is(err, ErrRDPInvalidRequest) {
		t.Fatalf("oversized lease = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Issue(ctx, request, now, time.Minute); err == nil {
		t.Fatal("cancelled context unexpectedly issued capability")
	}
}

func TestRDPServiceBindsCapabilityToActorAccountAndSlotLeases(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	material, _ := NewRDPMaterial("127.0.0.1", 3389, "opaque", "random", "", false)
	service, _ := NewRDPService(rdpAuthorizerFake{material: material})
	issued := RDPAuthorization{AccountID: "account-1", RequestID: "request-1", Actor: "ui", AccountLeaseID: "account-lease-1", SlotLeaseID: "slot-lease-1", SlotID: "pool-001", EnvironmentGeneration: 4}
	capability, err := service.Issue(context.Background(), issued, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(context.Background(), capability, "ui", now); !errors.Is(err, ErrRDPUnauthorized) {
		t.Fatalf("unbound resolve = %v", err)
	}
	if _, err := service.ResolveBound(context.Background(), capability, issued, now); err != nil {
		t.Fatal(err)
	}
	wrong := issued
	wrong.SlotLeaseID = "slot-lease-other"
	if _, err := service.ResolveBound(context.Background(), capability, wrong, now); !errors.Is(err, ErrRDPUnauthorized) {
		t.Fatalf("wrong slot lease resolve = %v", err)
	}
	if err := service.RevokeSlotLease(context.Background(), issued.SlotLeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveBound(context.Background(), capability, issued, now); !errors.Is(err, ErrRDPCapability) {
		t.Fatalf("resolve after slot revocation = %v", err)
	}
}

func TestRDPServiceRevokesAccountGenerationAndClose(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	material, _ := NewRDPMaterial("127.0.0.1", 3389, "opaque", "random", "", false)
	service, _ := NewRDPService(rdpAuthorizerFake{material: material})
	authorization := RDPAuthorization{AccountID: "account-1", RequestID: "request-1", Actor: "ui", AccountLeaseID: "account-lease-1", SlotLeaseID: "slot-lease-1", SlotID: "pool-001", EnvironmentGeneration: 4}
	capability, err := service.Issue(context.Background(), authorization, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeGeneration(context.Background(), authorization.SlotID, authorization.EnvironmentGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveBound(context.Background(), capability, authorization, now); !errors.Is(err, ErrRDPCapability) {
		t.Fatalf("generation revoke = %v", err)
	}
	capability, err = service.Issue(context.Background(), authorization, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeAccountLease(context.Background(), authorization.AccountLeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveBound(context.Background(), capability, authorization, now); !errors.Is(err, ErrRDPCapability) {
		t.Fatalf("account lease revoke = %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}
