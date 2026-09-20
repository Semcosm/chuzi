//go:build windows

// browser-launcher starts one browser process on a named Win32 desktop while
// remaining in the caller's Windows session. It is intentionally tiny and
// receives an argv list; no shell parsing is involved.
package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	args := os.Args[1:]
	if len(args) < 3 || args[0] != "--desktop" || args[2] != "--" || strings.TrimSpace(args[1]) == "" || strings.ContainsRune(args[1], '\x00') {
		fmt.Fprintln(os.Stderr, "usage: chuzi-browser-launcher.exe --desktop <window-station\\desktop> -- <browser> [args...]")
		os.Exit(2)
	}
	if err := launchOnDesktop(args[1], args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, "browser launch failed")
		os.Exit(1)
	}
}

func launchOnDesktop(desktop string, argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("browser command is required")
	}
	commandLine := windows.ComposeCommandLine(argv)
	commandUTF16, err := windows.UTF16FromString(commandLine)
	if err != nil {
		return err
	}
	application, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return err
	}
	desktop = normalizeDesktop(desktop)
	desktopUTF16, err := windows.UTF16PtrFromString(desktop)
	if err != nil {
		return err
	}
	stdin, _ := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	stdout, _ := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	stderr, _ := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	startup := windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desktopUTF16,
		Flags:      windows.STARTF_USESTDHANDLES,
		StdInput:   stdin,
		StdOutput:  stdout,
		StdErr:     stderr,
		ShowWindow: windows.SW_SHOWNORMAL,
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcess(application, &commandUTF16[0], nil, nil, true,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED, nil, nil, &startup, &process); err != nil {
		return err
	}
	defer windows.CloseHandle(process.Thread)
	defer windows.CloseHandle(process.Process)
	if err := windows.AssignProcessToJobObject(job, process.Process); err != nil {
		return err
	}
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		return err
	}
	if _, err := windows.WaitForSingleObject(process.Process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		os.Exit(int(code))
	}
	return nil
}

func normalizeDesktop(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `WinSta0\`) {
		return value
	}
	return `WinSta0\` + value
}
