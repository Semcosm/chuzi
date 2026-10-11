//go:build windows

package slotagent

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestValidateAgentCallerIdentityUsesNativeIdentityOnly(t *testing.T) {
	expected := filepath.Join(t.TempDir(), "chuzi.exe")
	config := PipeConfig{ServiceSID: agentServiceSID, ExpectedServiceExecutable: expected}
	valid := agentCallerIdentity{SID: agentServiceSID, SessionID: 0, Executable: expected, Authenticated: true}
	if err := validateAgentCallerIdentity(valid, config); err != nil {
		t.Fatalf("valid service identity rejected: %v", err)
	}
	for _, identity := range []agentCallerIdentity{
		{SID: "S-1-5-21-1", SessionID: 0, Executable: expected, Authenticated: true},
		{SID: agentServiceSID, SessionID: 1, Executable: expected, Authenticated: true},
		{SID: agentServiceSID, SessionID: 0, Executable: filepath.Join(filepath.Dir(expected), "other.exe"), Authenticated: true},
		{SID: agentServiceSID, SessionID: 0, Executable: expected},
	} {
		if !errors.Is(validateAgentCallerIdentity(identity, config), ErrAgentAuth) {
			t.Fatalf("identity %#v was accepted", identity)
		}
	}
}

func TestPipeConfigRejectsCallerSelectedTrustees(t *testing.T) {
	expected := filepath.Join(t.TempDir(), "chuzi.exe")
	if (PipeConfig{ServiceSID: "S-1-1-0", ExpectedServiceExecutable: expected}).validate() == nil {
		t.Fatal("arbitrary service SID accepted")
	}
	unclean := filepath.Dir(expected) + string(filepath.Separator) + ".." + string(filepath.Separator) + "chuzi.exe"
	for _, value := range []string{"", "relative.exe", unclean} {
		if (PipeConfig{ExpectedServiceExecutable: value}).validate() == nil {
			t.Fatalf("service executable %q accepted", value)
		}
	}
}

func TestValidAgentExecutablePathNormalizesWindowsSeparators(t *testing.T) {
	expected := "C:\\Program Files\\Chuzi\\chuzi.exe"
	actual := "\\\\?\\C:\\Program Files\\Chuzi\\chuzi.exe"
	if !validAgentExecutablePath(actual, expected) {
		t.Fatalf("equivalent Windows paths rejected: actual=%q expected=%q", actual, expected)
	}
	if validAgentExecutablePath("C:\\Program Files\\Other\\chuzi.exe", expected) {
		t.Fatal("same-name executable from another install accepted")
	}
}
