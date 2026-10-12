//go:build !windows

package servicehost

import "context"

type unsupportedPreflightSource struct{}

func NewWindowsPreflightSource(WindowsPreflightOptions) (FactSource, error) {
	return unsupportedPreflightSource{}, ErrHostUnavailable
}

func (unsupportedPreflightSource) Check(context.Context) ([]Fact, error) {
	return nil, ErrHostUnavailable
}
