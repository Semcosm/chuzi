package main

import (
	"strings"

	"github.com/Semcosm/chuzi/internal/slotwindows"
)

func localRDPUsername(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.Contains(name, "\\") {
		return name
	}
	return ".\\" + name
}

func validateRDPCredentialCleanup(target, expectedUsername string) error {
	if strings.TrimSpace(target) == "" || strings.TrimSpace(expectedUsername) == "" {
		return slotwindows.ErrSessionBrokerSecretInvalid
	}
	return nil
}

func rdpCredentialDeleteResult(result uintptr) error {
	if result == 0 {
		return slotwindows.ErrSessionUnavailable
	}
	return nil
}
