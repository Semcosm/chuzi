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
)

func EndpointPath(dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return `\\.\pipe\chuzi-core-` + hex.EncodeToString(sum[:8])
}

func Listen(ctx context.Context, path string) (net.Listener, error) {
	if ctx == nil || !strings.HasPrefix(path, `\\.\pipe\`) || strings.ContainsAny(path, "\r\n") {
		return nil, ErrInvalidTransport
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;OW)", InputBufferSize: DefaultMaxFrame, OutputBufferSize: DefaultMaxFrame})
}

func Dial(ctx context.Context, path string) (net.Conn, error) {
	if ctx == nil || !strings.HasPrefix(path, `\\.\pipe\`) {
		return nil, ErrInvalidTransport
	}
	return winio.DialPipeContext(ctx, path)
}
