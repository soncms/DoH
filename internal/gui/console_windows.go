//go:build windows && console

package gui

import (
	"fmt"
	"os"
)

func ReportStartupError(err error) { fmt.Fprintln(os.Stderr, "error:", err) }

func Run(string, bool) error { return fmt.Errorf("this build uses the CMD menu") }
