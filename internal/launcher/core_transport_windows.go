//go:build windows && !coretransport_native_test

package launcher

import (
	"context"

	"github.com/Semcosm/chuzi/internal/coretransport"
)

func connectCore(ctx context.Context, path string, config coretransport.Config) (*coretransport.Client, error) {
	return coretransport.Connect(ctx, path, config)
}
