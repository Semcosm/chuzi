//go:build windows

package coretransport

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func dialTestEndpoint(ctx context.Context, path string) (net.Conn, error) {
	return dialPipeUnverified(ctx, path)
}

func dialPipeUnverified(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, path)
}
