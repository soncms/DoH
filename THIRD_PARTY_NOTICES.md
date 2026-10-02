# Third-party notices

SonDNS embeds a small Windows runtime bundle so the distributed `SonDNS.exe`
does not require a separate helper installation.

## zapret2 / winws2

The `winws2.exe` engine and its Lua/filter assets are from the official
`bol-van/zapret2` and `bol-van/zapret-win-bundle` repositories. They are
distributed under the MIT License; the applicable text is included at
`third_party/zapret2/LICENSE.txt`.

## WinDivert

The embedded `WinDivert.dll` and `WinDivert64.sys` are from the official
WinDivert distribution. WinDivert is dual-licensed under LGPL-3.0-or-later or
GPL-2.0-or-later. Source and license information are available at
https://github.com/basil00/WinDivert.

## Cygwin runtime

`cygwin1.dll` is the Cygwin runtime required by the Windows `winws2` build.
It remains an unmodified upstream binary. Cygwin's license and source are
available from https://www.cygwin.com/.
