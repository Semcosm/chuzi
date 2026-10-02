//go:build windows

package environment

import (
	"golang.org/x/sys/windows"
)

const fileAttributeReparsePoint = 0x400

func isReparsePoint(path string) (bool, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(wide)
	if err != nil {
		return false, err
	}
	return attrs&fileAttributeReparsePoint != 0, nil
}
