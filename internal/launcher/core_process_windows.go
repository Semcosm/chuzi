//go:build windows

package launcher

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func serviceProcessMatches(pid int, expected string) bool {
	if pid <= 0 {
		return false
	}
	command := fmt.Sprintf("$p = Get-Process -Id %d -ErrorAction Stop; if ($null -eq $p.Path) { exit 1 }; Write-Output $p.Path", pid)
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command).Output()
	if err != nil {
		return false
	}
	return normalizeWindowsPath(strings.TrimSpace(string(output))) == normalizeWindowsPath(expected)
}

func stopServiceProcess(ctx context.Context, pid int) error {
	command := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	if err := command.Run(); err != nil {
		return fmt.Errorf("stop core: %w", err)
	}
	return nil
}

func normalizeWindowsPath(value string) string {
	path := strings.TrimSpace(strings.ReplaceAll(value, "/", "\\"))
	if stripped := strings.TrimPrefix(path, `\\?\UNC\`); stripped != path {
		path = `\\` + stripped
	} else if stripped := strings.TrimPrefix(path, `\\?\`); stripped != path {
		path = stripped
	}
	return strings.ToLower(strings.TrimRight(filepath.Clean(path), `\`))
}
