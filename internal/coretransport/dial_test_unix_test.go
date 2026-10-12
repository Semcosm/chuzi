//go:build !windows

package coretransport

import (
	"context"
	"net"
)

func dialTestEndpoint(ctx context.Context, path string) (net.Conn, error) {
	return Dial(ctx, path)
}

func connectTestClient(ctx context.Context, path string) (*Client, error) {
	return Connect(ctx, path, Config{})
}
