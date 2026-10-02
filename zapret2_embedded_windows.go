//go:build windows

package main

import _ "embed"

// These assets are extracted to ProgramData\SonDNS\zapret2 at runtime. The
// user-facing artifact remains a single SonDNS.exe; no helper installation is
// required on the destination machine.
//
//go:embed third_party/zapret2/winws2.exe
var embeddedZapret2WinWS []byte

//go:embed third_party/zapret2/cygwin1.dll
var embeddedZapret2Cygwin []byte

//go:embed third_party/zapret2/lua/zapret-lib.lua
var embeddedZapret2LibLua []byte

//go:embed third_party/zapret2/lua/zapret-antidpi.lua
var embeddedZapret2AntiDPILua []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.discord_media.txt
var embeddedZapret2DiscordFilter []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.stun.txt
var embeddedZapret2STUNFilter []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.wireguard.txt
var embeddedZapret2WireguardFilter []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.quic_initial_ietf.txt
var embeddedZapret2QUICFilter []byte
