//go:build !windows

package gui

import "errors"

func Run(string, bool) error { return errors.New("GUI is only supported on Windows") }
