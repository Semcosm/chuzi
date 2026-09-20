//go:build !windows

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
	processPath, err := filepath.EvalSymlinks("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		output, psErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		if psErr != nil {
			return false
		}
		return strings.TrimSpace(string(output)) == filepath.Base(expected)
	}
	expectedPath, err := filepath.EvalSymlinks(expected)
	if err != nil {
		expectedPath = filepath.Clean(expected)
	}
	return filepath.Clean(processPath) == filepath.Clean(expectedPath)
}

func stopServiceProcess(ctx context.Context, pid int) error {
	command := exec.CommandContext(ctx, "kill", "-TERM", strconv.Itoa(pid))
	if err := command.Run(); err != nil {
		return fmt.Errorf("stop core: %w", err)
	}
	return nil
}
