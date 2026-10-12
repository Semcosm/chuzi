package coretransport

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
)

func TestConnectionAuthorizerReducesErrorsToUnauthorized(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := (ConnectionAuthorizerFunc(func(context.Context, net.Conn) error { return errors.New("native detail") })).Authorize(context.Background(), left); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize() = %v, want ErrUnauthorized", err)
	}
	if err := (ConnectionAuthorizerFunc(func(context.Context, net.Conn) error { return nil })).Authorize(context.Background(), right); err != nil {
		t.Fatalf("authorized caller rejected: %v", err)
	}
}

func TestCallerPolicyRejectsUnauthenticatedAndForeignExecutables(t *testing.T) {
	root := t.TempDir()
	policy := CallerPolicy{ServiceSID: "S-1-5-80-123", LauncherRoot: root}
	valid := CallerIdentity{SID: "S-1-5-21-operator", SessionID: 3, Executable: filepath.Join(root, "chuzi-launcher.exe"), Authenticated: true, Interactive: true}
	if err := policy.Validate(valid); err != nil {
		t.Fatalf("valid launcher rejected: %v", err)
	}
	for _, value := range []CallerIdentity{
		valid,
		{SID: valid.SID, SessionID: valid.SessionID, Executable: filepath.Join(root, "chuzi.exe"), Authenticated: true, Interactive: true},
		{SID: valid.SID, SessionID: valid.SessionID, Executable: filepath.Join(t.TempDir(), "launcher.exe"), Authenticated: true, Interactive: true},
		{SID: valid.SID, SessionID: valid.SessionID, Executable: valid.Executable, Authenticated: false, Interactive: true},
		{SID: valid.SID, SessionID: 0, Executable: valid.Executable, Authenticated: true, Interactive: true},
	} {
		if value.Executable == valid.Executable && value.Authenticated && value.SessionID == valid.SessionID {
			continue
		}
		if err := policy.Validate(value); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("identity %#v error = %v", value, err)
		}
	}
	if err := policy.Validate(CallerIdentity{SID: policy.ServiceSID, Authenticated: true, Service: true}); err != nil {
		t.Fatalf("service caller rejected: %v", err)
	}
}
