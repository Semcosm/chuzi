package slotwindows

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestProfilePolicyShellCommandIsExact(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	policy, err := NewProfilePolicy(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	command, err := policy.ShellCommand()
	if err != nil {
		t.Fatal(err)
	}
	want := "powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File \"" + filepath.Join(runtimeRoot, SessionShellRelativePath) + "\""
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
	if err := ValidateSessionShellCommand(policy.RuntimeRoot, command); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePolicyRejectsUnsafeRootsAndCommands(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	unsafeNUL := runtimeRoot + "\x00"
	rootDirectory := filepath.VolumeName(runtimeRoot) + string(filepath.Separator)
	traversalRoot := filepath.Dir(runtimeRoot) + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(runtimeRoot)
	for _, root := range []string{
		``, rootDirectory, traversalRoot, unsafeNUL,
	} {
		if _, err := NewProfilePolicy(root); !errors.Is(err, ErrProfilePolicyPath) {
			t.Fatalf("root %q accepted with err %v", root, err)
		}
	}
	policy, err := NewProfilePolicy(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(runtimeRoot, SessionShellRelativePath)
	for _, command := range []string{
		"cmd.exe /c whoami",
		"powershell.exe -NoLogo -NoProfile -NonInteractive -Command whoami",
		"powershell.exe -ExecutionPolicy Bypass -File \"" + scriptPath + "\"",
		"powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy AllSigned -File \"" + filepath.Join(runtimeRoot, "other.ps1") + "\"",
	} {
		if err := ValidateSessionShellCommand(policy.RuntimeRoot, command); !errors.Is(err, ErrProfilePolicyCommand) {
			t.Fatalf("command %q accepted with err %v", command, err)
		}
	}
}

func TestProfilePolicyRejectsUnknownFieldsAndIsIdempotentShape(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	data, err := json.Marshal(ProfilePolicy{RuntimeRoot: runtimeRoot})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ParseProfilePolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := json.Marshal(map[string]string{"runtime_root": runtimeRoot, "command": "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseProfilePolicy(unknown)
	if !errors.Is(err, ErrProfilePolicy) || second.RuntimeRoot != "" {
		t.Fatalf("unknown field result = %#v, err %v", second, err)
	}
	firstCommand, _ := policy.ShellCommand()
	repeatedCommand, _ := policy.ShellCommand()
	if firstCommand != repeatedCommand {
		t.Fatal("repeated policy derivation was not idempotent")
	}
}
