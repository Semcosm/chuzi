//go:build !windows

package servicehost

import (
	"context"
)

func ValidateServiceToken(Identity) error { return ErrHostUnavailable }
func FixedDataRoot() (string, error)      { return "", ErrHostUnavailable }
func ValidateFixedDataRoot(string) error  { return ErrHostUnavailable }

type unsupportedController struct{}

func NewController(TrustedRuntime, string) (Controller, error) {
	return unsupportedController{}, ErrHostUnavailable
}
func NewControllerWithDataRoot(TrustedRuntime, string, string) (Controller, error) {
	return unsupportedController{}, ErrHostUnavailable
}
func (unsupportedController) Start(context.Context) (Status, error) {
	return Status{}, ErrHostUnavailable
}
func (unsupportedController) Stop(context.Context) error { return ErrHostUnavailable }
func (unsupportedController) Status(context.Context) (Status, error) {
	return Status{State: StateStopped}, nil
}

func Install(context.Context, TrustedRuntime, string) error { return ErrHostUnavailable }
func InstallWithDataRoot(context.Context, TrustedRuntime, string, string) error {
	return ErrHostUnavailable
}
func Uninstall(context.Context) error { return ErrHostUnavailable }
func UninstallWithDataRoot(context.Context, TrustedRuntime, string, string) error {
	return ErrHostUnavailable
}
