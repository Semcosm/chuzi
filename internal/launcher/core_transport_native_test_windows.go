//go:build windows && coretransport_native_test

package launcher

import (
	"context"

	"github.com/Semcosm/chuzi/internal/coretransport"
)

func connectCore(ctx context.Context, path string, _ coretransport.Config) (*coretransport.Client, error) {
	return coretransport.ConnectUnverifiedForNativeTest(ctx, path)
}
