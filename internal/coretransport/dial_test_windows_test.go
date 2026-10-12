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

func connectTestClient(ctx context.Context, path string) (*Client, error) {
	conn, err := dialPipeUnverified(ctx, path)
	if err != nil {
		return nil, err
	}
	client, err := NewClient(conn, Config{})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}
