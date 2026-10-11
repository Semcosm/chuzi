//go:build !windows

package main

import "github.com/Semcosm/chuzi/internal/coretransport"

func newCoreAuthorizer() (coretransport.ConnectionAuthorizer, error) { return nil, nil }
