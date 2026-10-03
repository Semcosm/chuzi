//go:build windows

package environment

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func sameResolvedPath(a, b string) bool {
	normalizedA, okA := longWindowsPath(a)
	normalizedB, okB := longWindowsPath(b)
	return okA && okB && strings.EqualFold(normalizedA, normalizedB)
}

func longWindowsPath(path string) (string, bool) {
	windowsPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetLongPathName(windowsPath, &buffer[0], uint32(len(buffer)))
	if err != nil || length == 0 || int(length) >= len(buffer) {
		return "", false
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:length])), true
}
