package main

import (
	"errors"
	"testing"

	"github.com/Semcosm/chuzi/internal/slotwindows"
)

func TestLocalRDPUsernameFailsClosedWithoutAccountIdentity(t *testing.T) {
	if got := localRDPUsername("  "); got != "" {
		t.Fatalf("empty local RDP username = %q", got)
	}
	if got := localRDPUsername("slot-user"); got != ".\\slot-user" {
		t.Fatalf("local RDP username = %q", got)
	}
	if got := localRDPUsername("DOMAIN\\slot-user"); got != "DOMAIN\\slot-user" {
		t.Fatalf("qualified RDP username = %q", got)
	}
}

func TestRDPCredentialCleanupRequiresOwnedTargetAndIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		target   string
		username string
	}{
		{name: "missing target", username: ".\\slot-user"},
		{name: "missing identity", target: "TERMSRV/127.0.1.2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRDPCredentialCleanup(test.target, test.username); !errors.Is(err, slotwindows.ErrSessionBrokerSecretInvalid) {
				t.Fatalf("cleanup validation error = %v", err)
			}
		})
	}
	if err := validateRDPCredentialCleanup("TERMSRV/127.0.1.2", ".\\slot-user"); err != nil {
		t.Fatalf("valid cleanup identity rejected: %v", err)
	}
}

func TestRDPCredentialDeleteResultReportsNativeFailure(t *testing.T) {
	if err := rdpCredentialDeleteResult(0); !errors.Is(err, slotwindows.ErrSessionUnavailable) {
		t.Fatalf("failed native deletion error = %v", err)
	}
	if err := rdpCredentialDeleteResult(1); err != nil {
		t.Fatalf("successful native deletion error = %v", err)
	}
}
