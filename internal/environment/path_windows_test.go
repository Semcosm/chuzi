//go:build windows

package environment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSameResolvedPathAcceptsShortNameAlias(t *testing.T) {
	longPath := filepath.Join(t.TempDir(), "chuzi-long-package-root")
	if err := os.Mkdir(longPath, 0700); err != nil {
		t.Fatal(err)
	}
	widePath, err := windows.UTF16PtrFromString(longPath)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetShortPathName(widePath, &buffer[0], uint32(len(buffer)))
	if err != nil || length == 0 || int(length) >= len(buffer) {
		t.Skip("short path aliases are unavailable")
	}
	shortPath := filepath.Clean(windows.UTF16ToString(buffer[:length]))
	if strings.EqualFold(filepath.Clean(longPath), shortPath) {
		t.Skip("filesystem did not create a short path alias")
	}
	resolved, err := filepath.EvalSymlinks(shortPath)
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedPath(resolved, shortPath) {
		t.Fatal("short and long forms of the same package path were rejected")
	}
	if err := os.WriteFile(filepath.Join(longPath, "worker.mjs"), []byte("bundled worker"), 0600); err != nil {
		t.Fatal(err)
	}
	resource, err := ResolveRegularResource(shortPath, "worker.mjs")
	if err != nil {
		t.Fatalf("bundled resource through short-name root: %v", err)
	}
	if data, err := os.ReadFile(resource); err != nil || string(data) != "bundled worker" {
		t.Fatalf("bundled resource = %q, error = %v", data, err)
	}
}
