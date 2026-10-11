//go:build !windows

package main

import "github.com/Semcosm/chuzi/internal/servicehost"

func runSessionBroker() error {
	return servicehost.ErrHostUnavailable
}
