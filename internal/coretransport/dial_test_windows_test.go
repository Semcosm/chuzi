//go:build windows

package coretransport

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func dialTestEndpoint(ctx context.Context, path string) (net.Conn, error) {
	return dialPipeUnverified(ctx, path)
}

func dialPipeUnverified(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeAccessImpLevel(
		ctx,
		path,
		uint32(windows.GENERIC_READ|windows.GENERIC_WRITE),
		winio.PipeImpLevelImpersonation,
	)
}
