//go:build !windows

package main

import "errors"

func runGUI() error { return errors.New("GUI is only supported on Windows") }
