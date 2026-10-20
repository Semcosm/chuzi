//go:build windows

package coretransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const corePipePrefix = "\\\\.\\pipe\\chuzi-core-"

// CorePipeSecurityDescriptor keeps the endpoint out of Everyone and Users.
// The service and administrators have full control; an interactive launcher
// caller receives only pipe read/write access. Request actor text is still
// treated as untrusted data by the Core API.
const CorePipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

func EndpointPath(dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return corePipePrefix + hex.EncodeToString(sum[:8])
}

func validEndpoint(path string) bool {
	if !strings.HasPrefix(path, corePipePrefix) || strings.ContainsAny(path, "\r\n\"") {
		return false
	}
	suffix := strings.TrimPrefix(path, corePipePrefix)
	if len(suffix) != 16 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func Listen(ctx context.Context, path string) (net.Listener, error) {
	if ctx == nil || !validEndpoint(path) {
		return nil, ErrInvalidTransport
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: CorePipeSecurityDescriptor, InputBufferSize: DefaultMaxFrame, OutputBufferSize: DefaultMaxFrame})
}

func Dial(ctx context.Context, path string) (net.Conn, error) {
	return DialWithConfig(ctx, path, Config{})
}

func DialWithConfig(ctx context.Context, path string, config Config) (net.Conn, error) {
	if ctx == nil || !validEndpoint(path) {
		return nil, ErrInvalidTransport
	}
	conn, err := winio.DialPipeAccessImpLevel(
		ctx,
		path,
		uint32(windows.GENERIC_READ|windows.GENERIC_WRITE),
		winio.PipeImpLevelImpersonation,
	)
	if err != nil {
		return nil, err
	}
	if err := validatePipeServerIdentity(conn, config.ExpectedServerExecutable, config.ExpectedServerPID); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
