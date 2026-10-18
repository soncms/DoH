//go:build windows

package resources

import (
	"embed"
)

// Keep copyright and license notices with a single-EXE distribution. These
// documents are also available before extraction in the executable itself.
//
//go:embed LICENSE NOTICE THIRD_PARTY_NOTICES.md PORTABLE.md licenses
var PortableNotices embed.FS

//go:embed assets/runtime-sha256.json
var RuntimeAssetManifest []byte

// Use the same upstream WinDivert build as the embedded winws2 engine. Keeping
// one DLL/SYS pair avoids ABI and driver-service mismatches between the DNS
// interceptor and the DPI backend.
//
//go:embed third_party/zapret2/WinDivert.dll
var EmbeddedWinDivertDLL []byte

//go:embed third_party/zapret2/WinDivert64.sys
var EmbeddedWinDivertSYS []byte

// These assets are extracted under runtime/embedded beside SonDNS.exe. The
// user-facing artifact remains a single SonDNS.exe; no helper installation is
// required on the destination machine.
//
//go:embed third_party/zapret2/winws2.exe
var EmbeddedZapret2WinWS []byte

//go:embed third_party/zapret2/cygwin1.dll
var EmbeddedZapret2Cygwin []byte

//go:embed third_party/zapret2/lua/zapret-lib.lua
var EmbeddedZapret2LibLua []byte

//go:embed third_party/zapret2/lua/zapret-antidpi.lua
var EmbeddedZapret2AntiDPILua []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.discord_media.txt
var EmbeddedZapret2DiscordFilter []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.stun.txt
var EmbeddedZapret2STUNFilter []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.wireguard.txt
var EmbeddedZapret2WireguardFilter []byte

//go:embed third_party/zapret2/windivert.filter/windivert_part.quic_initial_ietf.txt
var EmbeddedZapret2QUICFilter []byte
