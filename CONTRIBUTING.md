# Contributing to SonDNS

SonDNS is a Windows amd64 application under active development. Reproducible
bug reports, compatibility results, documentation and focused patches are
welcome. There is no stable public release or promised support schedule yet.

## Report a problem

Search existing issues first. Include SonDNS version/EXE SHA-256, Windows build,
DNS/DPI mode, reproduction steps and expected/actual behavior. Redact domains,
private addresses, profile IDs, credentials and local paths. Review configs/logs
before attaching them. Use [SECURITY.md](SECURITY.md) for vulnerabilities.

## Develop

Install Go 1.27.2 and PowerShell 7 on Windows amd64. From the repository root:

```powershell
go test -timeout 2m ./...
go vet ./...
./scripts/build.ps1 -Version local-dev
```

Keep application files beside the EXE. Do not introduce profile-directory
storage, a separate downloaded DNS core, hidden elevation or automatic
replacement of an existing service. Preserve saved custom upstreams and service
ownership, folder ACL and IPC authorization checks.

## Test safely

Core race tests require a compatible C compiler:

```powershell
go test -race -timeout 2m ./...
```

Native widget tests run separately because of Walk/win's integer-resource
pointer checkptr limitation:

```powershell
$env:SONDNS_UI_TEST = '1'
try { go test -run 'TestNative|TestAppIcon|TestProviderApply' -count=1 -timeout 2m ./... }
finally { Remove-Item Env:SONDNS_UI_TEST }
```

Filesystem security tests use a temporary protected folder and restricted token.
Set `SONDNS_FILESYSTEM_TEST_PARENT` to a trusted fixed-drive root such as `C:\`
when elevated. Do not change permissions on existing application/user folders.

The isolated SCM startup test creates/deletes a uniquely named **stopped**
service backed by a protected Windows binary. It never starts a process, opens
WinDivert or changes the actual SonDNS service:

```powershell
$env:SONDNS_SCM_STARTUP_TEST = '1'
try { go test -run '^TestNativeSCMStartupTransaction$' -count=1 -v -timeout 2m ./... }
finally { Remove-Item Env:SONDNS_SCM_STARTUP_TEST }
```

Actual service/driver/interception tests have separate opt-ins. Run them on a
clean disposable Windows machine with no existing SonDNS/WinDivert service.
Never remove an active installation merely to make a test pass. Follow the
manual service/UAC/migration checks in [RELEASING.md](RELEASING.md).

Native-source verifier fixtures need only Python's standard library:

```powershell
python -I -B -m unittest discover -s scripts -p 'test_*.py'
```

## Submit a patch

Keep patches focused, format Go with `gofmt` and explain the problem, resulting
behavior and validation. Add meaningful regression tests for behavioral/security
changes. Record skipped tests and reasons. Avoid incidental vendor churn.

Runtime changes need updated pins, source/build/license evidence and isolated
integration tests. Rebuilt drivers need valid signing/testing. See
[RUNTIME_PROVENANCE.md](RUNTIME_PROVENANCE.md).

You agree to license original contributions under Apache-2.0. Retain third-party
attribution/terms and submit only code and data you are authorized to share.
