//go:build !windows

package coretransport

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func EndpointPath(dataDir string) string { return filepath.Join(dataDir, "core.sock") }

func Listen(ctx context.Context, path string) (net.Listener, error) {
	if ctx == nil || filepath.IsAbs(path) == false || path == "" {
		return nil, ErrInvalidTransport
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, ErrInvalidTransport
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, ErrInvalidTransport
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
			return nil, ErrInvalidTransport
		}
		probe, probeErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if probeErr == nil {
			_ = probe.Close()
			return nil, ErrEndpointBusy
		}
		if err := os.Remove(path); err != nil {
			return nil, ErrInvalidTransport
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrInvalidTransport
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen core socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, ErrInvalidTransport
	}
	return &unlinkListener{Listener: listener, path: path}, nil
}

func Dial(ctx context.Context, path string) (net.Conn, error) {
	if ctx == nil || !filepath.IsAbs(path) || path == "" {
		return nil, ErrInvalidTransport
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

type unlinkListener struct {
	net.Listener
	path string
}

func (l *unlinkListener) Close() error {
	err := l.Listener.Close()
	removeErr := os.Remove(l.path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if err != nil {
		return err
	}
	return removeErr
}
