//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// runPrivileged keeps the UI unprivileged. Service-management commands are
// relaunched through the normal Windows UAC consent flow when needed; the UI
// can then poll SCM for the resulting state.
func runPrivileged(action func() error) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	if token.IsElevated() {
		return action()
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	quoted := make([]string, len(os.Args)-1)
	for i, arg := range os.Args[1:] {
		quoted[i] = quoteWindowsArgument(arg)
	}
	args := strings.Join(quoted, " ")
	argPtr, _ := windows.UTF16PtrFromString(args)
	if err := windows.ShellExecute(0, verb, file, argPtr, nil, windows.SW_SHOWNORMAL); err != nil {
		return err
	}
	return nil
}

// quoteWindowsArgument follows CommandLineToArgvW escaping rules so a config
// path containing spaces survives the UAC relaunch unchanged.
func quoteWindowsArgument(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\"") {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteRune(r)
			backslashes = 0
		default:
			if backslashes > 0 {
				b.WriteString(strings.Repeat("\\", backslashes))
				backslashes = 0
			}
			b.WriteRune(r)
		}
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat("\\", backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}
