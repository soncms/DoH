# Source map

SonDNS builds one Windows executable from `cmd/sondns`. Source directories
describe responsibilities; they do not create additional runtime installations.

| Path | Responsibility |
| --- | --- |
| `cmd/sondns/` | Small executable entry point. |
| `internal/app/` | Choose SCM service, desktop/tray or administration commands. |
| `internal/gui/` | Native Walk window, tray, request table and user actions. |
| `internal/service/` | Runtime composition, SCM ownership, portable filesystem protection, service migration/rollback and tray startup. The optional console runtime is selected only with the `console` build tag. |
| `internal/engine/` | Encrypted DNS transports, bootstrap lookup, DNSSEC adapter, resolver and upstream replacement. |
| `internal/control/` | Authenticated local named-pipe server/client and request/response contracts. |
| `internal/config/` | JSON configuration, defaults, providers and shared validation. |
| `internal/requestlog/` | Bounded in-memory request/attempt ring. |
| `internal/packet/` | IPv4/IPv6 UDP DNS parsing and response construction. |
| `internal/buildinfo/` | Version injected by the linker. |
| `internal/testutil/` | Shared synthetic DNS, partial-pipe and restricted-token fixtures, imported only by tests. |
| `resources_windows.go` | Embed the existing runtime payloads, notices and hash manifest. Go embed paths must be relative to their package directory, so this small resource package stays beside the original asset paths. |
| `third_party/` | Local upstream replacements and pinned native runtime assets; their licenses and patch records remain intact. |
| `scripts/`, `.github/workflows/` | Build, source/notice packaging and verification. |

The dependency direction is `app -> gui -> service -> engine -> control`.
Configuration, request logs, packet parsing and embedded resources are shared
leaf packages. The GUI uses service operations; the service runtime does not
import Walk or the GUI package. Migration staging, binding changes and rollback
stay together because they form one service transaction.

Tests stay next to each package so they can verify its internal invariants.
The disposable lifecycle harness is compiled from `internal/service`; it drives
the actual separately built desktop executable and service. GUI widget tests
run from `internal/gui`. Build and source packaging scripts include `cmd` and
`internal`, and the license inventory follows `./cmd/sondns`'s linked dependencies.
The root Windows resource object supplies the icon/manifest to the application
and native GUI tests through the embedded-resource package.

Run `go test ./...` and `go vet ./...` on an appropriate Windows test machine.
Use `scripts/build.ps1` for the GUI executable; a direct Go build must target
`./cmd/sondns`. The service/driver, account and live-network test opt-ins described
in `CONTRIBUTING.md` and `docs/CLEAN_WINDOWS_TESTS.md` still apply.
