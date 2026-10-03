//go:build windows

package slotwindows

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
