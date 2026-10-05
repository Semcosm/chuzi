//go:build windows

package environment

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const fileAttributeReparsePoint = 0x400

func isReparsePoint(path string) (bool, error) {
	attrs, err := windowsFileAttributes(path)
	if err != nil {
		return false, err
	}
	return attrs&fileAttributeReparsePoint != 0, nil
}

func isReparsePath(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	volume := filepath.VolumeName(abs)
	current := volume
	remainder := strings.TrimPrefix(abs, volume)
	if strings.HasPrefix(remainder, `\`) || strings.HasPrefix(remainder, `/`) {
		current += string(filepath.Separator)
		remainder = strings.TrimLeft(remainder, `\/`)
	}
	if current == "" {
		current = string(filepath.Separator)
	}
	if reparse, err := isReparsePoint(current); err != nil || reparse {
		return reparse, err
	}
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool { return r == '\\' || r == '/' }) {
		current = filepath.Join(current, component)
		reparse, err := isReparsePoint(current)
		if err != nil || reparse {
			return reparse, err
		}
	}
	return false, nil
}

func windowsFileAttributes(path string) (uint32, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	attrs, err := windows.GetFileAttributes(wide)
	if err != nil {
		return 0, err
	}
	return attrs, nil
}
