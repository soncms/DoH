# Known Go vulnerability checks

Run from Windows amd64 with the toolchain in `go.mod`, Python 3.10+ and network
access to Go module downloads, the official Go vulnerability database and
OSV's public `https://api.osv.dev/v1/querybatch` endpoint:

```powershell
./scripts/scan-go-vulnerabilities.ps1
```

Use a new `-OutputDirectory` when specifying one. The script refuses reuse so a
failed scan cannot inherit an earlier passing summary. It pins govulncheck v1.9.0,
uses source/symbol analysis for the default native application tags, and records
the Go target/toolchain, source commit/dirty state, database timestamp, loaded
packages and module/replacement provenance. It temporarily sets read-only module
resolution and disables inherited workspaces/build tags, then restores the caller's
Go environment. It neither executes SonDNS nor installs a service or driver.

Windows verification runs this gate on each normal workflow trigger and retains
the raw JSON stream, stderr, provenance and summary with its test reports. The
parser independently validates scanner identity/scope, required SBOM and finding
traces, process completion metadata and the recorded raw-output hash.
Scanner failure, empty/malformed JSON, missing required records or an affected imported
package/reachable symbol fails the job. JSON scanner exit status alone is not a
passing gate. Parser tests include actual CLI nonzero exits for findings/errors.

Module-only advisory matches remain visible for review; they do not establish
that an affected package is imported. For example, a match against unmaintained
`golang.org/x/crypto/openpgp` must not be described as affected DNS code when that
package is absent from the recorded package graph. There is no advisory allowlist
or severity exemption. Counts describe finding records, not unique CVEs.

The report records upstream versions and local replacements together. Source
analysis can identify a local replacement by its filesystem path and lose its
upstream version. The gate therefore also queries OSV using each local
replacement's canonical public Go module name and upstream baseline version.
Only those names/versions are sent, without source, local paths or credentials.
`local-baselines.json` retains the complete request/response, timestamp and hashes.
The parser checks coverage and response counts; errors, incomplete responses and
pagination cannot silently pass. Any baseline advisory match blocks pending
source review or an upstream update. A baseline match does not establish that
the modified code is exploitable; an unmatched baseline does not certify patches.

SonDNS's dnsproxy/dnscrypt cancellation patches and Walk fixes are not blanket advisory
exemptions. Check any matched advisory against the actual patched source; a local
replacement or absent version cannot prove it fixed. Preserve `SONDNS_PATCHES.md`
and upstream correspondence evidence when changing those sources.

This is a dated known-advisory/source-graph check. It does not cover unknown bugs,
reflective/dynamic call paths the analyzer cannot resolve, native Cygwin/WinDivert/
zapret binaries, external helpers, publisher signing or the Windows integration
and privacy/license review. It does not certify a production release. Review the
database timestamp and retained findings alongside the exact release source and
artifact hashes. Offline rebuilds remain offline; the signing command does not
silently fetch this online scanner on the publisher's signing machine.

For an offline independent report replay, call `scripts/go_vulnerability_report.py`
with the retained `--input`, `--metadata`, `--modules`, `--packages`,
`--baseline-evidence` and a new `--output` path. Omit `--query-baselines`; replay
validates the recorded lookup rather than making a new request.
