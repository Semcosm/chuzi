package slotwindows

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticodePolicyRejectsUnboundOrMalformedSigner(t *testing.T) {
	policy := AuthenticodePolicy{
		Format: authenticodePolicyFormat, PublisherSubjectPrefix: "CN=Semcosm",
		AllowedThumbprints: []string{strings.Repeat("A", 40)},
		RequiredTimestamp:  true, Revocation: "timestamp-online-cached-offline",
		OfflineValidation:       "cached-chain-and-timestamp",
		RequiredFiles:           []string{"session-shell.ps1"},
		ProductionRequiredFiles: []string{"session-shell.ps1"},
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if !policy.Allows(strings.Repeat("a", 40), "CN=Semcosm Code Signing") {
		t.Fatal("expected bound signer")
	}
	policy.ArtifactKind = "payload"
	if err := policy.Validate(); err != nil {
		t.Fatalf("resolved payload policy rejected: %v", err)
	}
	policy.ArtifactKind = "unexpected"
	if !errors.Is(policy.Validate(), ErrAuthenticodePolicy) {
		t.Fatal("accepted invalid artifact scope")
	}
	policy.ArtifactKind = ""
	if policy.Allows(strings.Repeat("B", 40), "CN=Semcosm Code Signing") {
		t.Fatal("accepted wrong thumbprint")
	}
	if policy.Allows(strings.Repeat("A", 40), "CN=other") {
		t.Fatal("accepted wrong publisher")
	}
	policy.AllowedThumbprints = nil
	if policy.Allows(strings.Repeat("A", 40), "CN=Semcosm Code Signing") {
		t.Fatal("accepted empty production allowlist")
	}
	policy.Revocation = "none"
	if !errors.Is(policy.Validate(), ErrAuthenticodePolicy) {
		t.Fatal("accepted weak revocation policy")
	}
}

func TestLoadAuthenticodePolicyRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authenticode-policy.json")
	data := "{\"format\":\"chuzi-authenticode-policy/v1\",\"publisher_subject_prefix\":\"CN=Semcosm\",\"allowed_thumbprints\":[],\"required_timestamp\":true,\"revocation\":\"timestamp-online-cached-offline\",\"offline_validation\":\"cached-chain-and-timestamp\",\"unexpected\":true}"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthenticodePolicy(path); !errors.Is(err, ErrAuthenticodePolicy) {
		t.Fatalf("unknown field error = %v", err)
	}
}
