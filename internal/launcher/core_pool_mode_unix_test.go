//go:build !windows

package launcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCorePoolModeUsesFixedArgumentsAndRedactsFailures(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantError string
	}{
		{"saved", `printf '%s' '{"configured_pool_mode":"windows","status":"restart_required"}'`, ""},
		{"cleanup", `printf '%s\n' 'chuzi: pool_cleanup_required' >&2; exit 1`, "pool_cleanup_required"},
		{"raw failure", `printf '%s\n' 'chuzi: pool_cleanup_required password=secret /private/profile' >&2; exit 1`, "pool_mode_save_failed"},
		{"malformed", `printf '%s' 'not JSON'`, "pool_mode_save_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			manager, err := NewCoreManager(root)
			if err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nprintf '%s\n' \"$@\" > calls.txt\n" + tc.response + "\n"
			if err := os.WriteFile(manager.ServicePath, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			output, err := manager.SavePoolMode(context.Background(), "windows", "pool with spaces", 7)
			if tc.wantError == "" {
				if err != nil || !strings.Contains(string(output), "restart_required") {
					t.Fatalf("output=%s error=%v", output, err)
				}
			} else if err == nil || err.Error() != tc.wantError || len(output) != 0 {
				t.Fatalf("output=%s error=%v", output, err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Join([]string{"-config", manager.ConfigPath, "-configure-pool-mode", "windows", "-pool-id", "pool with spaces", "-expected-revision", "7", ""}, "\n")
			if string(calls) != want {
				t.Fatalf("unexpected service arguments: %q", calls)
			}
		})
	}
}
