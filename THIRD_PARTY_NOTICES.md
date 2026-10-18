# Third-party notices

Original SonDNS code is licensed under Apache-2.0; see `LICENSE` and `NOTICE`.
Third-party components keep their own copyrights and licenses. The single EXE
includes these documents and extracts them beside the application, including
the `licenses/` directory, when the GUI opens or `verify` is run.

## Linked Go code

`licenses/modules.json` lists the modules linked into the GUI edition, their
versions and the accompanying license files. `scripts/generate-licenses.ps1`
collects those texts from the actual dependency sources. The Go runtime license
is in `licenses/go-toolchain/LICENSE`.

AdGuard dnsproxy v0.86.0 is **Apache-2.0** and is locally patched to preserve
caller cancellation/deadlines during DNSCrypt TCP fallback. Its source, original
license, upstream hashes and patch description are in `third_party/dnsproxy/`.
Walk and its Win32 bindings use BSD
licenses. Walk is locally modified; its original license remains in
`third_party/walk/LICENSE`, with a copy in `licenses/go/`.

AdGuard DNSCrypt v0.0.3 is **Apache-2.0** and is locally patched to interrupt
client-owned encrypted and certificate-query sockets on context cancellation.
Its original license, upstream file hashes and patch description are in
`third_party/dnscrypt/`; certificate signatures and stamp pins are unchanged.

## Embedded Windows runtime

The binary and Lua/filter hashes are pinned in `assets/runtime-sha256.json`.
See `RUNTIME_PROVENANCE.md` for the verified bundle commit and original paths.

| Component | Version | Terms and included text |
| --- | --- | --- |
| zapret2 / winws2 and Lua/filter assets | v1.0.5.2 | MIT; `licenses/runtime/zapret2/LICENSE.txt` |
| WinDivert DLL and driver | v2.2.2 | Dual LGPLv3 or GPLv2; the upstream license text, including the complete license options, is in `licenses/runtime/windivert/LICENSE` |
| Cygwin runtime | 3.4.10 | The Cygwin DLL sources are LGPLv3-or-later with the linking exception in `licenses/runtime/cygwin/CYGWIN_LICENSE`; GPL/LGPL and Newlib notices are included alongside it |

WinDivert is used as a shared library. Cygwin is used by the separate winws2
helper. Including either in this EXE does not relicense it as SonDNS code.
Downloaded runtime license texts and their hashes are recorded in
`licenses/runtime/provenance.json`.

Upstream source references:

- zapret Windows bundle: https://github.com/bol-van/zapret-win-bundle/tree/6eb463a6758fb48cd101bc55dfd057e6e9d98af1
- WinDivert: https://github.com/basil00/WinDivert/tree/v2.2.2
- Cygwin: https://github.com/cygwin/cygwin/tree/cygwin-3.4.10

## Public binary release preparation

This portable build is a local preview. Before distributing binaries publicly,
package and make available the corresponding source, build scripts and any
patches for the exact shipped WinDivert/Cygwin binaries, with the required
license notices and a practical way to rebuild or replace the libraries. A
generic upstream link or this notice alone does not complete these duties.
WinDivert's official archives and Cygwin's exact 3.4.10-1 source/reference
packages are pinned in `assets/native-source-lock.json` and included by source
packaging. The Cygwin package contains the exact bundled DLL; all 6,476 source
blobs/modes match the recipe's upstream commit. Its original build recipe is
included, but a Cygwin recompilation has not been tested. The bundled WinDivert
driver matches that release; the DLL's two-byte PE-header modification is
reproducible with `scripts/reproduce-windivert-dll.ps1`. A replacement DLL can
be compiled from the included source with `scripts/rebuild-windivert.ps1`.
See `RUNTIME_PROVENANCE.md` for the exact evidence and build limits.

SonDNS currently verifies and repairs extracted runtime files against embedded
hashes. Modified libraries therefore require rebuilding SonDNS with the
replacement assets and updated hash manifest. The public source package must
include this build path and all corresponding application source needed for it.
