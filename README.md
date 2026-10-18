# SonDNS

SonDNS is a portable encrypted DNS utility for Windows. One native
`SonDNS.exe` contains the AdGuard dnsproxy core, a desktop/tray interface,
WinDivert DNS interception and an optional zapret2 DPI helper.

[Tiếng Việt](docs/README.vi.md) · [Portable guide](PORTABLE.md) ·
[Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Contributing](CONTRIBUTING.md)

## Status

SonDNS is being prepared for its first public release. Current local builds
are unsigned previews. Source packaging, offline application rebuilds, native
source correspondence, core race tests, native widgets and an isolated stopped
SCM fixture have been verified locally. [GitHub Actions](https://github.com/soncms/DoH/actions)
runs Windows verification; the separate disposable service/driver lifecycle
workflow requires manual dispatch. Check each report's commit and artifact
hashes before applying its result to a build. Alternate-account UAC, reboot,
live migration and the remaining release cases still require manual evidence.
See the [clean Windows test guide](docs/CLEAN_WINDOWS_TESTS.md).

## Run

1. Put `SonDNS.exe` in a dedicated folder on a fixed local drive.
2. Double-click it to open the desktop window and tray icon.
3. Use **Manage…** for UAC elevation, then **Install & start** to install the
   service and enable interception.

The GUI initially reads status without changing an existing service. The SYSTEM
service requires a folder protected against ordinary-user writes, including
its ancestors. **Create portable copy…** creates a protected folder at a trusted
location such as `F:\`, copies the EXE/settings and opens that copy. It refuses
existing destinations. See [PORTABLE.md](PORTABLE.md).

Configuration, logs, helper HOME/TEMP, runtime and license notices stay beside
the EXE. SonDNS does not store these in AppData, ProgramData or Program Files.
Windows still registers the service/driver and optional machine-wide tray
startup. Uninstall the service before moving its folder; uninstalling preserves
application files and data.

Tray startup keeps the selected config path. An implicit GUI launch follows
its owned service config, while an explicit different config cannot change
that service's DNS/DPI/startup settings.

Closing the window hides it in the tray. **Exit** closes the GUI while a running
service continues resolving DNS. Normal commands refuse a service in another
folder. **Migrate existing service…** is a separate confirmed action with a
backup and guarded rollback; unsafe legacy folders can remain stopped after a
failure. See the portable guide before switching an existing installation.

## Features and limits

- DoH, DoT, DoQ and DNS stamps through AdGuard dnsproxy.
- IPv4/IPv6 outbound UDP/53 interception through WinDivert, requiring admin
  access. It does not intercept TCP/53, applications' own DoH/DoT, mDNS/LLMNR
  or DNS inside a VPN tunnel.
  Fragments, IPv6 authentication headers and active routing headers pass through
  unchanged; SonDNS does not terminate IPsec or IPv6 source routing.
- Native request table with results, latency, copy/clear actions and at most
  200 recent requests in memory. SonDNS has no local DNS cache.
- Optional embedded zapret2/winws2 DPI profiles. DNS interception stays active
  when DPI bypass is disabled; helper failure can leave DNS running with fail-open.
- Machine-wide service/tray startup and authenticated local control IPC.

Presets use SonCMS and a shared NextDNS profile; see [PRIVACY.md](PRIVACY.md).
Choose upstreams you trust. SonDNS is not an anonymity service or a VPN.

## Build and verify

Application code is grouped by responsibility under [`internal/`](internal):
GUI, Windows service/runtime, DNS engine, IPC, configuration, request logs and
packet parsing. [`cmd/sondns`](cmd/sondns) contains the executable entry point.
Tests live beside the feature they verify; shared fixtures are in
`internal/testutil`. See the [source map](docs/ARCHITECTURE.md).

Use Windows amd64, Go 1.27.2 and PowerShell 7:

```powershell
go test -timeout 2m ./...
go vet ./...
./scripts/build.ps1 -Version local-preview
```

The GUI output is `dist/SonDNS-Portable/SonDNS.exe`, with SHA-256/build metadata.
Default builds have no CMD menu. The icon/manifest compiler is pinned in
`go.mod`. Public builds require publisher Authenticode signing and manual checks.

Package the application, modified Walk, vendored Go/build tools and pinned
WinDivert/Cygwin source/reference archives:

```powershell
./scripts/package-source.ps1 -Version local-review
```

Source collection also needs Python 3.10+ and network access or a verified cache.
Rebuilding from an extracted source package needs neither Python nor Go module
downloads. See [RELEASING.md](RELEASING.md) for offline builds, signing, library
replacement and publication gates, and [CONTRIBUTING.md](CONTRIBUTING.md) for tests.

## License

Original SonDNS code is [Apache-2.0](LICENSE). Third-party components retain
their own terms; see [NOTICE](NOTICE), [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
and [RUNTIME_PROVENANCE.md](RUNTIME_PROVENANCE.md). Packaging components in one
EXE does not relicense them as original SonDNS code.
