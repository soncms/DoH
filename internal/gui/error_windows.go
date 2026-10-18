//go:build windows && !console

package gui

import (
	"fmt"
	"os"

	"github.com/lxn/walk"
)

func ReportStartupError(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	if len(os.Args) == 1 || os.Args[1] == "gui" {
		walk.MsgBox(nil, "SonDNS", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
}
