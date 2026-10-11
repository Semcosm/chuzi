//go:build windows && coretransport_native_test

package coretransport

import (
	"context"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// ConnectUnverifiedForNativeTest is an explicit test-only client boundary for
// a foreground Core process. Production callers must use Connect, which
// authenticates the service as the LocalSystem ChuziCore host.
func ConnectUnverifiedForNativeTest(ctx context.Context, path string) (*Client, error) {
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
	client, err := NewClient(conn, Config{})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := client.Hello(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
