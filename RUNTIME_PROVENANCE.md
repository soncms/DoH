# Embedded Windows runtime provenance

Verified on 2026-10-16 against the official Windows bundle:

- Repository: https://github.com/bol-van/zapret-win-bundle
- Pinned commit: `6eb463a6758fb48cd101bc55dfd057e6e9d98af1`
- Source directory: `zapret-winws/`
- zapret2/winws2: v1.0.5.2 (https://github.com/bol-van/zapret2/releases/tag/v1.0.5.2)
- WinDivert: v2.2.2; the driver reports the 2.2 file-version family.
- Cygwin: 3.4.10, retained as supplied by the bundle.

All ten embedded files match the Git blob hashes in that pinned bundle: the
four Windows binaries, two Lua files, and four WinDivert filter fragments.
Their SHA-256 hashes are pinned in `assets/runtime-sha256.json`; run
`scripts/verify-assets.ps1` before building. The embedded driver has a valid
Authenticode signature. Keep these files together as the tested Windows bundle.

The existing license is in `third_party/zapret2/LICENSE.txt`. This provenance file is documentation
and is not an additional embedded runtime asset.

## WinDivert release/source correspondence

The bundled driver is byte-identical to
`WinDivert-2.2.2-A/x64/WinDivert64.sys` in the official v2.2.2 release archive.
The bundled DLL has exactly two changed bytes compared with that archive's
x64 DLL: PE `DllCharacteristics` gains `DYNAMIC_BASE` (0x40) at file offset
0xDE, and the PE checksum changes from 0xFC9C to 0xFCDC at 0xD8. Every raw PE
section is identical, including `.text`, imports, exports and relocations.
This describes static bytes; it does not claim a live ASLR/interception test.

`scripts/reproduce-windivert-dll.ps1` accepts only the exact official x64 DLL,
sets the flag, recomputes the PE checksum and verifies that the entire result
matches the shipped DLL hash. It refuses an existing output. The runtime DLL
and driver in this repository have not been replaced.

`assets/native-source-lock.json` pins the official reference archive and the
WinDivert v2.2.2 source commit
`1789526ecfb9ff5397c94f9f54c1a3dc2fb60440`. Source packaging includes both
archives under `native-sources/`, with the upstream source/build scripts and
licenses intact. Collection checks each archive hash and reproduces the DLL
before recording the package inventory. No downloaded DLL/driver is loaded.

To replay the exact header modification, extract the official archive's x64
DLL to a scratch directory and run:

```powershell
./scripts/reproduce-windivert-dll.ps1 -InputDll ./upstream/WinDivert.dll -OutputDll ./patched/WinDivert.dll
```

To compile a replacement DLL from the included source on Windows, install an
x86_64 MinGW-w64 compatible GCC/Clang and strip toolchain and choose a new output directory:

```powershell
./scripts/rebuild-windivert.ps1 -SourceArchive ./native-sources/WinDivert-source-1789526ecfb9ff5397c94f9f54c1a3dc2fb60440.zip -OutputDirectory C:/build/WinDivert-rebuilt
```

The script uses the amd64 DLL commands from upstream `mingw-build.sh` with an
explicit dynamic-base flag and a forced `stddef.h` include for modern headers.
MSVCRT supplies compiler-generated memory-copy primitives in modern Clang builds.
It records the compiler and resulting hash. This
does not promise byte identity across compilers, build or load a driver, or
replace SonDNS assets. Use the library replacement procedure in `RELEASING.md`
after reviewing and testing a rebuild. For driver builds, the source archive
also contains the original `msvc-build.bat`, WDK project and `wddk-build.bat`;
Microsoft's driver-signing requirements apply to any replacement driver.

## Cygwin release/source correspondence

The pinned `cygwin-3.4.10-1.tar.xz` contains `usr/bin/cygwin1.dll` with SHA-256
`103104a52e5293ce418944725df19e2bf81ad9269b9a120d71d39028e821499b`, exactly
matching the embedded runtime. Its matching `cygwin-3.4.10-1-src.tar.xz` includes
the original `cygwin.cygport` recipe and `newlib-cygwin-3.4.10.tar.bz2` source.
The recipe identifies upstream commit
`89a1e4a573d40b233011402f42630b970ff71291`.

The historical packages are available only over HTTP from the Cygwin Time
Machine snapshot at `ctm.crouchingtigerhiddenfruitbat.org`, dated
2024-01-31 23:48:56. Their SHA-256 pins are in `assets/native-source-lock.json`.
Historical setup metadata signatures were unavailable and have not been
verified. The independent evidence is the pre-existing runtime DLL hash and
comparison of every source Git blob/mode with the complete upstream GitHub
HTTPS tree, retained as `assets/cygwin-3.4.10-git-tree.json`. All 6,476 archived
source files match. Only `.gitattributes`, `.gitignore`,
`newlib/doc/.gitignore` and `winsup/doc/.gitignore` are absent from the snapshot.

Collection checks both archive hashes, the DLL, the recipe, the nested source
archive, the pinned tree evidence and every source blob/mode. It reads archives
without extracting paths or executing downloaded code. To repeat this check:

```powershell
python -I -B ./scripts/verify_cygwin_source.py --archives ./native-sources
```

To build a replacement, use an isolated x86_64 Cygwin build environment. Install
`cygport` and the dependencies named by `BUILD_REQUIRES` in the original recipe.
From a new scratch directory in the Cygwin shell, unpack the verified source
package (paths below are examples):

```sh
tar -xf /cygdrive/c/build/SonDNS-source/native-sources/cygwin-3.4.10-1-src.tar.xz
cd cygwin-3.4.10-1.src
cygport ./cygwin.cygport prep compile install package
```

Use the included source snapshot; `fetch` is not needed. The recipe regenerates
the `winsup` autotools files, configures the build and records the 3.4.10-1
release string. Its aggregate GPL package label also covers GPL utilities; it
does not change the DLL's LGPLv3-or-later and linking-exception terms.
Review `winsup/CYGWIN_LICENSE`, `winsup/COPYING.LIB` and `COPYING.NEWLIB` in the
source for the component-specific terms. This Cygwin build procedure has not
been executed here and does not promise byte identity with the original build.

Do not replace the system Cygwin DLL or copy a new DLL over a running helper.
Copy a reviewed replacement into the application's source assets and follow
the rebuild procedure in `RELEASING.md`; test it in a separate portable folder.
The original runtime pins remain unchanged. Native source correspondence is
ready; signing and the manual service/driver/UAC release checks still remain.
