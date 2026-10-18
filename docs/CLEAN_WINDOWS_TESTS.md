# Disposable Windows test kit

This optional maintainer kit contains SonDNS.exe, a prebuilt test harness, its
runner, hashes and notices. It is separate from the single-EXE application.
Both EXEs are built from the same verified source ZIP. No Go/Python installation
or public DNS resolver is needed on the test VM. Do not run lifecycle tests on
your working DNS machine. Take a clean Windows amd64 VM snapshot first, or use
the explicitly dispatched disposable GitHub-hosted job described below.

The harness has a loopback DNSCrypt upstream with a freshly pinned key and
synthetic `sondns-test.invalid` answers. It never forwards requests to a public
resolver, changes the system trust store or bypasses certificate verification.
A connected virtual NIC with an IPv4 route and enabled IPv6/link-local address
is needed. Internet DNS and a provider's globally routed IPv6 subnet are not
needed: the runner adds a temporary documentation IPv6 source, one /128 route
and two synthetic static neighbors. It preserves adapter DNS, default routes,
bindings and interface policy. The source uses SkipAsSource, so unrelated traffic
does not select it. Conflicting existing entries are never replaced.

## Run in the VM

Extract the kit to a short local path and open PowerShell as administrator.
Windows PowerShell 5.1 or PowerShell 7 is sufficient for the runner. Inspect
the supplied hashes and source before trusting an unsigned preview/harness.

```powershell
./run-clean-windows-tests.ps1 -Mode Check
./run-clean-windows-tests.ps1 -Mode Run -DisposableVM
```

Check mode records preflight only. Run mode requires no existing SonDNS service,
WinDivert driver, SonDNS process or SonDNS HKLM Run value. It refuses to replace
them. `-DisposableVM` records the operator's confirmation; detection of a
hypervisor cannot prove the machine is disposable. The runner does not install
Windows features, create a VM or reboot the computer.

The lifecycle test creates a protected unique folder under the system drive,
installs/starts/stops/restarts/uninstalls the real service and driver, checks
custom config/Run bindings, launches the GUI without changes, tests local UDP
and TCP DNS, and checks transparent IPv4 UDP/53 interception. It also sends A and
AAAA queries over real UDP/IPv6 sockets to a documentation destination and a
scoped link-local destination, requiring the synthetic upstream's answers while
the service runs. The same four routed queries must time out before installation
and after cleanup; routing/socket errors do not count as successful negative
controls. The runner records setup/final network snapshots and requires removal
of its address, host route and neighbors with interface policy unchanged. This
does not test public IPv6 upstream connectivity or every extension header/MTU.
It uses saved
manual/automatic startup modes without rebooting. It holds an intercepted query
at the encrypted upstream while stopping, checks bounded shutdown and native
error logs, and exercises the embedded DPI process startup, forced exit,
automatic restart and shutdown. Process termination is restricted to a handle
whose image path matches the unique fixture. A quiet stop includes 500 ms without
harness DNS queries; Windows background DNS is not suppressed.

The same opt-in test installs a running automatic service with custom settings,
dependencies and delayed startup. It opens the built GUI's real service menu,
checks the confirmation text and default No button, cancels, and requires exact
service/tray/config snapshots and running DNS to remain unchanged. A real source
config lock after confirmation must show a validation error without creating a
backup or restarting the source helper. Each GUI process is reaped by its owned
process handle. These procedures require a non-skipped pass on the tested commit.
An exclusive lock on the candidate EXE forces a real SCM startup error; the test
requires the previous binding and running DNS to recover, exact destination
config bytes and tray value/type to survive, and all source/backup hashes to match.
The subsequent successful migration is confirmed through the built GUI. Its
success status must identify the actual backup directory and its controls must
show the imported custom provider and TCP DPI mode before the usual backend
SCM/config/backup/DNS assertions. This covers an already elevated session only.
A subsequent successful migration checks imported settings, custom config/tray
paths, unrelated SCM fields, UDP/TCP DNS and the old/new embedded DPI processes.
Cleanup calls uninstall from the currently owning fixture EXE and verifies both
helper paths, service, driver and tray are absent. Reports retain journals,
manifests and logs; inert backed-up EXE/runtime `.bin` files are not uploaded.

It removes its
fixture when cleanup succeeds, and retains it when recovery is needed. Logs and JSON results
are saved in `reports/` under the kit folder. A skipped lifecycle test is a
failure of the runner, never a claimed pass.

On timeout or failed cleanup, inspect `result.json`, its SCM paths and logs.
Do not delete files underneath a surviving service/driver. Restore the VM
snapshot after collecting the synthetic test report. Share the report, exact
EXE/source hashes and Windows build; do not infer a pass from the exit code
alone. `fullReleaseValidationPassed` remains false even after a lifecycle pass.

## Still manual

The lifecycle additionally creates two uniquely named temporary local Windows
accounts (standard user with an interactive logon and separate administrator
with a batch logon), authenticates them with random in-memory passwords, and
exercises their real tokens against the running
service. Standard-user config reads, production SCM status/config queries and
IPC log snapshots must work; config write, SCM
mutation handles, pipe-server creation and IPC mutations must be denied. The
separate elevated administrator must be able to open management handles and
clear the actual service log. Config bytes must stay unchanged and DNS must
remain usable. A live windowless copy of the test harness under the standard
account additionally checks the production PID handoff wait from the other
administrator's token. Waiting must be denied before preparation. The child
starts suspended and receives exactly four explicitly duplicated anonymous
event handles with wait/signal access only, through a SID-readable manifest.
It inherits no caller handles or CI environment. These synchronization handles
do not grant access to the child process and do not use named-event ACLs.
The child
uses the production helper on its own process to grant Administrators only
SYNCHRONIZE, verifies exact DACL restoration, then prepares again. The other
admin must wait successfully while terminate, memory-write and DACL-change
access remain denied. Both accounts
and the owned child must be removed/exited and reported in `accounts.json`.
This procedure requires an explicit pass on the tested commit. It does not
operate the Manage button, credential prompt, secure desktop or GUI/tray under
the other account, and does not establish the full UAC handoff flow.
Batch logon avoids using an identification-only UAC linked token as an
impersonation token; it is a backend access check, not interactive elevation.
Native service monitoring uses SCM plus IPC `snapshot`; the optional IPC
`state` operation is implemented by the console engine, not the native engine.
The actual credential prompt/cancellation remains manual. The production launch
path restores process permissions if ShellExecute fails or is cancelled;
after a successful launch the wait-only grant remains until the old GUI exits.

This does not establish alternate-account UAC, reboot/tray startup,
all migration failure stages, DPI effectiveness/application compatibility,
power-loss recovery or publisher signing. Complete RELEASE_TEST_PLAN.md in
the kit and RELEASING.md in the source package before release.

## GitHub-hosted alternative

`.github/workflows/windows-service-lifecycle.yml` is an optional manual workflow
on a GitHub-hosted `windows-2022` VM. It never runs on push or pull requests.
The workflow must first be published on the repository's default branch for
GitHub to offer manual dispatch. Publishing it is separate from running it.
Review the complete source changes before pushing them; the workflow builds
the selected remote commit, not unpublished local edits or a previous EXE.

In **Actions > Disposable Windows service lifecycle > Run workflow**, select
the reviewed branch and explicitly enable `confirm_disposable`. This authorizes
installing the actual service/driver on the temporary runner and using Windows
Actions minutes. A private repository may incur charges after its included
allowance. No separate VM, publisher certificate or repository secrets are
required. The job has a read-only repository token, does not retain checkout
credentials, and uploads reports rather than application/source ZIPs.

All dependency and corresponding-source downloads finish before interception.
The job packages and verifies the source ZIP, builds the GUI EXE and precompiled
harness offline from that same extraction, and records hashes and runner/Windows
metadata. The extracted path includes spaces. The existing kit runner still
requires elevation, refuses existing SonDNS/WinDivert installations and treats
a skipped test as failure. Build failure, skipped execution or a missing result
does not establish a lifecycle pass.

GitHub destroys the hosted VM at the end of the job; this is the isolation and
recovery assumption instead of a local restore snapshot. Do not change this
workflow to a self-hosted runner or run its lifecycle commands on a working PC.
The kit runner attempts cleanup before report upload. Reports are retained for
seven days. Forced cancellation or a surviving interception service may prevent
upload; such a run supplies no successful evidence and the VM must be discarded.
Download the reports promptly and review `result.json`, `workflow-result.json`,
EXE/source hashes and logs. Even a real lifecycle pass keeps
`fullReleaseValidationPassed=false` and leaves the manual cases above open.

## Rebuild the kit

After building the application from the actual extracted source ZIP, keep
offline vendor settings and run its packaging script:

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOFLAGS = '-mod=vendor'
./scripts/package-windows-test-kit.ps1 -ApplicationDirectory C:\build\portable -OutputDirectory C:\build\test-kits
```

The source manifest and application build-info must match. The packager never
runs the lifecycle test. The source ZIP remains a separate release artifact;
retain it alongside the application and test-kit archive for reproducibility.
