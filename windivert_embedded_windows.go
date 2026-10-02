//go:build windows

package main

import _ "embed"

// Use the same upstream WinDivert build as the embedded winws2 engine. Keeping
// one DLL/SYS pair avoids ABI and driver-service mismatches between the DNS
// interceptor and the DPI backend.
//
//go:embed third_party/zapret2/WinDivert.dll
var embeddedWinDivertDLL []byte

//go:embed third_party/zapret2/WinDivert64.sys
var embeddedWinDivertSYS []byte
