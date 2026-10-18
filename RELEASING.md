# Building and preparing a SonDNS release

SonDNS is a Windows amd64 native GUI/tray application. The distributable is
one `SonDNS.exe`; it extracts its runtime, configuration and legal notices
beside itself. See [PORTABLE.md](PORTABLE.md) for service folder permissions.

## Application source archive

Use Windows, PowerShell 7 and Go 1.27.2. Creating or collecting a source package
also requires Python 3.10+ on PATH (standard library only). The application and
offline EXE rebuild do not require Python. The resource compiler is pinned as
a Go tool in `go.mod`; it is included when vendoring dependencies.

```powershell
./scripts/package-source.ps1 -Version local-review
```

The repository includes `native-source-cache/`, containing the four exact pinned
native source/reference archives. Packaging uses that cache by default, verifies
the existing hashes and binary/source correspondence, and places the verified
archives in the source ZIP's `native-sources/` directory without duplicating the
cache. This avoids relying on the historical Cygwin mirror being reachable from
a build runner. An explicit `-NativeSourceCacheDirectory` overrides the default.
`.gitattributes` preserves exact upstream runtime and source-tree metadata bytes
on Windows checkouts; do not normalize their line endings or change their pins
to accept a checkout conversion.

This creates `dist/source/SonDNS-source-local-review.zip`, an extracted source
directory and a checksum. An existing destination is refused. The package
contains the current application source, the modified Walk source, build
scripts, tests, license notices, pinned runtime assets and vendored Go/tool
dependencies. It also includes pinned WinDivert/Cygwin source and reference
archives, Cygwin's original cygport build recipe, upstream source-tree evidence
and the reproducible WinDivert DLL header patch. It includes uncommitted source changes; the manifest records
that fact and hashes each packaged file. It omits local configs, logs, `.git`,
agent/editor files, secrets outside the explicit source roots and prior build
outputs. Review the archive before any public upload. Private files placed
inside a source directory still require human review.

Collection verifies the exact shipped WinDivert/Cygwin binaries against their
reference packages and checks all 6,476 Cygwin source blobs and executable modes
against the pinned upstream Git tree. Cygwin source correspondence is verified;
recompiling that DLL with its original toolchain has not been tested. This is
source/provenance evidence, not a declaration that every release requirement
is satisfied. Keep it private until the public-release checklist below is complete.

To verify and build an extracted archive without Git, network module downloads
or an existing Go module cache:

```powershell
cd C:/build/SonDNS-source-local-review
./scripts/verify-source.ps1
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOFLAGS = '-mod=vendor'
$env:GOMODCACHE = 'C:/build/empty-modcache'
./scripts/build.ps1 -Version local-review -OutputDirectory C:/build/SonDNS-Portable
```

Use an installed toolchain matching `go.mod`; `GOTOOLCHAIN=local` prevents
automatic toolchain downloads. `build-info.json` records the source manifest
hash and its original Git provenance. Unsigned builds from an archive are
functional rebuilds; byte identity with a signed artifact is not promised.
The build checks the source snapshot before compiling. Resource generation
creates `rsrc_windows_amd64.syso`; `verify-source.ps1 -AllowBuildOutputs` permits
this generated file and `dist/` outputs when rechecking an already-built tree.
Keep an untouched extraction for comparison.

## Replacing embedded libraries

Replace the relevant files in `third_party/zapret2/`, then update their SHA-256
entries in `assets/runtime-sha256.json` and run `scripts/verify-assets.ps1`.
Keep a compatible WinDivert DLL/driver pair and review source/license/version
records for the replacement. Rebuild the EXE with `scripts/build.ps1` using the
application sources above. The original release's source manifest no longer
matches after a source/asset change. Preserve it as a record, remove
`source-package.json` from the modified tree and rebuild. The build records
unknown/dirty provenance rather than the original snapshot's identity. To
generate a new source package, put the modified sources in a Git checkout.

Runtime extraction repairs files that differ from the embedded hash pins.
Replacing only an extracted DLL is therefore insufficient. Rebuilding embeds
the replacement and its new pins. Windows driver-signing requirements still
apply; a rebuilt driver does not inherit the upstream driver's signature.

## Verification

The Windows CI workflow builds and checks source packages without publishing
EXEs or source archives. Core tests use the race detector. Native widget tests
run separately because the Walk/win integer-resource pointer path currently
fails Go's race/checkptr instrumentation. Filesystem security tests use a
temporary protected folder and a restricted Windows token.

`SONDNS_SCM_STARTUP_TEST=1` enables an isolated startup-transaction test. It
creates a uniquely named stopped SCM fixture, checks narrow startup writes,
dependency removal and delayed-start rollback, then deletes it. No fixture
process is started and the actual SonDNS service/driver is not changed. This
does not replace full service lifecycle or clean-machine UAC testing.

An optional [disposable Windows test kit](docs/CLEAN_WINDOWS_TESTS.md) can be
built from the same extracted verified source ZIP as the application. It
contains a precompiled harness and local pinned encrypted upstream, so the VM
does not need Go or public resolvers. Packaging/preflight is not a lifecycle
result; the separate manual/UAC/migration/reboot cases still require evidence.

Without a local VM, the separate manually dispatched
`windows-service-lifecycle.yml` workflow can run the kit on a disposable
GitHub-hosted Windows VM. See the test-kit guide for publication/dispatch,
Actions-minute costs, retained reports and the hosted isolation assumption.
It does not run on ordinary push/PR verification and cannot certify a full
production release. Authenticode signing is a SonDNS release quality gate;
it is not a stated requirement of 1Password's open-source program.

Service and real interception tests are opt-in. Run them on a clean disposable
Windows machine with no SonDNS/WinDivert installation, never by replacing a
developer's active DNS service. Manually exercise UAC cancellation/approval,
portable-copy handoff, tray startup for ordinary accounts and the installed
service lifecycle before a production release. Record the exact EXE hash,
Windows build and results.

## Public-release checklist

- Provide corresponding source, patches and build scripts for the exact
  shipped native libraries, with evidence matching each binary to its source
  package. A version tag or an upstream homepage alone is insufficient.
- Provide the application source archive and practical library replacement
  instructions alongside the binary, satisfying the selected upstream terms.
- Complete clean-machine service/driver/UAC tests and migration/rollback
  verification. Confirm configuration/log/runtime files stay portable.
- Review security and dependency findings, provenance, licenses and the archive
  contents. Preserve the test reports and source manifest for this version.
- Enable GitHub Private Vulnerability Reporting and test SECURITY.md's reporting
  link before publication. Review shared DNS presets and the privacy disclosure
  with the maintainer.
- Sign with the publisher's real Authenticode certificate and verify the
  timestamp, EXE checksum and final artifact metadata.

`scripts/build.ps1 -Release` enforces signing, tests, asset integrity and the
native corresponding-source readiness flag. `-Sign`
can still sign a preview without declaring it a public release. These checks
do not establish that the legal or manual integration checklist has been
completed. Do not publish a build solely because this command succeeded.
No workflow automatically creates a release, pushes code or changes visibility.
