//go:build windows

package main

import _ "embed"

//go:embed WinDivert.dll
var embeddedWinDivertDLL []byte

//go:embed WinDivert64.sys
var embeddedWinDivertSYS []byte

