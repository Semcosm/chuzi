//go:build !windows

package main

import (
	"context"
	"errors"

	"github.com/Semcosm/chuzi/internal/servicehost"
)

func runServiceHost(context.Context, serviceOptions) error {
	return errors.Join(servicehost.ErrHostUnavailable, errors.New("SCM service mode is only supported on Windows"))
}
