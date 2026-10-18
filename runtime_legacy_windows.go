//go:build windows && !console

package main

import (
	"fmt"
	"log"
	"os"

	"github.com/lxn/walk"
)

const consoleBuild = false

func consoleCLI([]string) error           { return nil }
func portableRuntimeDir() (string, error) { return "", nil }
func newRuntimeEngine(c Config, logger *log.Logger) (runtimeEngine, *Resolver, error) {
	return NewDNSProxyEngine(c, logger)
}

func reportStartupError(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	if len(os.Args) == 1 || os.Args[1] == "gui" {
		walk.MsgBox(nil, "SonDNS", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
}
