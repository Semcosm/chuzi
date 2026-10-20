//go:build windows && coretransport_native_test

package launcher

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Semcosm/chuzi/internal/coretransport"
)

func connectCore(ctx context.Context, path string, _ coretransport.Config) (*coretransport.Client, error) {
	// Hosted foreground smoke uses isolated data instead of the fixed service
	// root. This override exists only in explicitly tagged test launchers.
	if root := os.Getenv("CHUZI_CORETRANSPORT_NATIVE_TEST_DATA_ROOT"); root != "" {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, ErrInvalidPath
		}
		path = coretransport.EndpointPath(root)
	}
	return coretransport.ConnectUnverifiedForNativeTest(ctx, path)
}
