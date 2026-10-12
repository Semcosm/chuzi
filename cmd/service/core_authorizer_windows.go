//go:build windows

package main

import (
	"os"
	"path/filepath"

	"github.com/Semcosm/chuzi/internal/coretransport"
	"github.com/Semcosm/chuzi/internal/servicehost"
)

func newCoreAuthorizer() (coretransport.ConnectionAuthorizer, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filepath.Clean(executable))
	serviceSID, err := servicehost.ServiceSIDString(servicehost.ServiceName)
	if err != nil {
		return nil, err
	}
	return coretransport.IdentityAuthorizer{
		Provider: coretransport.NewWindowsPipeIdentityProvider(),
		Policy: coretransport.CallerPolicy{
			ServiceSID:         serviceSID,
			LauncherRoot:       root,
			LauncherExecutable: filepath.Join(root, "chuzi-launcher.exe"),
		},
	}, nil
}
