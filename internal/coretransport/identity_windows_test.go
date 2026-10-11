//go:build windows && coretransport_native_test

package coretransport

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsPipeIdentityProviderRoundTrip(t *testing.T) {
	path := EndpointPath(filepath.Join(t.TempDir(), "identity"))
	listener, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverConn := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, acceptErrValue := listener.Accept()
		if acceptErrValue != nil {
			acceptErr <- acceptErrValue
			return
		}
		serverConn <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The test process is not a SYSTEM-owned installed Core executable, so
	// bypass the production peer-image check while exercising caller identity.
	client, err := dialPipeUnverified(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var conn net.Conn
	select {
	case conn = <-serverConn:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer conn.Close()
	identity, err := NewWindowsPipeIdentityProvider().Identity(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !identity.Authenticated || !identity.Interactive || identity.SessionID == 0 || identity.SID == "" || !sameWindowsPath(identity.Executable, executable) {
		t.Fatalf("unexpected identity: %#v executable=%q", identity, executable)
	}
}

func sameWindowsPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func TestValidServerExecutablePathRejectsSameNameOtherInstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "CorePayload")
	expected := filepath.Join(root, "chuzi.exe")
	if !validServerExecutablePath(expected, expected) {
		t.Fatal("expected installed service path rejected")
	}
	if validServerExecutablePath(filepath.Join(t.TempDir(), "CorePayload", "chuzi.exe"), expected) {
		t.Fatal("same-name executable from another installation accepted")
	}
	if validServerExecutablePath(expected, filepath.Join(root, "chuzi-launcher.exe")) {
		t.Fatal("different executable accepted")
	}
}
