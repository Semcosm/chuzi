//go:build !windows

package coretransport

import (
	"context"
	"net"
)

func dialTestEndpoint(ctx context.Context, path string) (net.Conn, error) {
	return Dial(ctx, path)
}
