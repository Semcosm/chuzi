//go:build windows

package slotwindows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Semcosm/chuzi/internal/slot"
)

func TestStopSlotLeaseRequiresOwnedAgentAfterRestart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	lease := slot.Lease{LeaseID: "lease-1", SlotID: "slot-1", PoolID: "pool-1", RequestID: "request-1", AccountID: "account-1", Owner: "owner-1", EnvironmentGeneration: 1, AcquiredAt: now.Add(-2 * time.Minute), LastHeartbeat: now.Add(-90 * time.Second), ExpiresAt: now.Add(-time.Second)}
	p := &windowsProvisioner{agents: make(map[string]agentProcess)}
	if err := p.StopSlotLease(context.Background(), lease); !errors.Is(err, ErrCleanup) {
		t.Fatalf("missing post-restart agent = %v, want ErrCleanup", err)
	}
}

func TestManagedUserPrivilegeAllowed(t *testing.T) {
	for _, privilege := range []uint32{userPrivGuest, userPrivUser} {
		if !managedUserPrivilegeAllowed(privilege) {
			t.Fatalf("managed privilege %d was rejected", privilege)
		}
	}
	if managedUserPrivilegeAllowed(2) {
		t.Fatal("administrator privilege was accepted")
	}
}

func TestValidateOwnedTreeShapeAllowsManagedEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "job-slot")
	for _, name := range []string{"work", "tmp", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, "generation-000001", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ownership.json", "profile-access.json", "account.dpapi"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateOwnedTreeShape(root); err != nil {
		t.Fatalf("managed tree rejected: %v", err)
	}
}

func TestValidateOwnedTreeShapeRejectsUnknownRootEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "job-slot")
	for _, name := range []string{"work", "tmp", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, "generation-000001", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "unexpected.bin"), []byte("unknown"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateOwnedTreeShape(root); err == nil {
		t.Fatal("unknown root entry accepted for deletion")
	}
}

func TestRemoveSIDACEsPreservesOtherTrustees(t *testing.T) {
	sid := "S-1-5-21-100-200-300-400"
	sddl := "D:P(A;OICI;FA;;;SY)(D;;FA;;;" + sid + ")(A;;FA;;;BA)(A;OICI;FR;;;" + sid + ")"
	got, removed := removeSIDACEs(sddl, sid)
	if !removed {
		t.Fatal("managed trustee ACEs were not found")
	}
	if strings.Contains(got, ";;;"+sid+")") {
		t.Fatal("managed trustee ACE remained")
	}
	for _, trustee := range []string{";;;SY)", ";;;BA)"} {
		if !strings.Contains(got, trustee) {
			t.Fatalf("unrelated trustee %s was removed", trustee)
		}
	}
}

func TestRemoveSIDACEsIsIdempotent(t *testing.T) {
	sddl := "D:P(A;OICI;FA;;;SY)"
	got, removed := removeSIDACEs(sddl, "S-1-5-21-100-200-300-400")
	if removed || got != sddl {
		t.Fatalf("unchanged ACL = %q, removed=%t", got, removed)
	}
}

func TestRuntimeACLAllowsInheritedReadExecuteEntries(t *testing.T) {
	sddl := "D:PAI(A;OICI;0x001200a9;;;BU)(A;I;0x001200a9;;;BU)(A;OICI;FA;;;SY)"
	if !runtimeACLAllowsReadExecute(sddl, "S-1-5-21-100-200-300-400") {
		t.Fatal("safe inherited runtime ACL rejected")
	}
}

func TestRuntimeACLRejectsWriteAccessForBuiltinUsers(t *testing.T) {
	sddl := "D:PAI(A;OICI;0x001200a9;;;BU)(A;OICI;0x001301bf;;;BU)"
	if runtimeACLAllowsReadExecute(sddl, "S-1-5-21-100-200-300-400") {
		t.Fatal("runtime write ACL accepted")
	}
}

func TestRuntimeACLRejectsBroadWriteAccess(t *testing.T) {
	sddl := "D:PAI(A;OICI;0x001200a9;;;BU)(A;OICI;0x001301bf;;;WD)"
	if runtimeACLAllowsReadExecute(sddl, "S-1-5-21-100-200-300-400") {
		t.Fatal("runtime broad write ACL accepted")
	}
}

func TestProfileACLParsesUnpaddedModifyMask(t *testing.T) {
	sddl := "D:P(A;OICI;0x1301bf;;;S-1-5-21-100-200-300-400)"
	if !sddlHasAllowACE(sddl, "S-1-5-21-100-200-300-400", "OICI", uint32(fileModifyMask)) {
		t.Fatal("profile modify ACE with unpadded mask was rejected")
	}
}

func TestProfileRootACLRequiresNonInheritedTraverseOnlyACE(t *testing.T) {
	sid := "S-1-5-21-100-200-300-400"
	if !sddlHasAllowACE("D:P(A;;0x1200a0;;;"+sid+")", sid, "", uint32(fileProfileRootMask)) {
		t.Fatal("profile root traverse ACE was not recognized")
	}
	for _, rights := range []string{"FX", "GX"} {
		if !sddlHasAllowACE("D:P(A;;"+rights+";;;"+sid+")", sid, "", uint32(fileProfileRootMask)) {
			t.Fatalf("profile root %s ACE was not recognized", rights)
		}
	}
	if sddlHasAllowACE("D:P(A;OICI;0x1200a0;;;"+sid+")", sid, "", uint32(fileProfileRootMask)) {
		t.Fatal("inherited profile root ACE was accepted")
	}
	if sddlHasAllowACE("D:P(A;;0x1301bf;;;"+sid+")", sid, "", uint32(fileProfileRootMask)) {
		t.Fatal("profile root broad ACE was accepted")
	}
}

func TestProfileACLRejectsDenyACE(t *testing.T) {
	sddl := "D:P(D;OICI;0x1301bf;;;S-1-5-21-100-200-300-400)"
	if !sddlHasDenyACE(sddl, "S-1-5-21-100-200-300-400") {
		t.Fatal("profile deny ACE was not detected")
	}
}

func TestControlPlaneDenySDDLParsing(t *testing.T) {
	if !sddlHasFullDeny("D:P(D;OICI;0x001f01ff;;;S-1-5-21-100-200-300-400)", "S-1-5-21-100-200-300-400") {
		t.Fatal("full deny ACE was not recognized")
	}
	if sddlHasFullDeny("D:P(A;OICI;0x001200a9;;;S-1-5-21-100-200-300-400)", "S-1-5-21-100-200-300-400") {
		t.Fatal("read/execute ACE was treated as a full deny")
	}
}

func TestValidateProfilePathRequiresDerivedAccountDirectory(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "profiles")
	valid := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.MkdirAll(valid, 0o700); err != nil {
		t.Fatal(err)
	}
	provisioner := &windowsProvisioner{options: Options{DataDir: dataDir}}
	if _, err := provisioner.validateProfilePath(valid); err != nil {
		t.Fatalf("derived profile rejected: %v", err)
	}
	for _, value := range []string{filepath.Join(root, "other"), filepath.Join(valid, "nested")} {
		if _, err := provisioner.validateProfilePath(value); err == nil {
			t.Fatalf("arbitrary profile path accepted: %q", value)
		}
	}
}
