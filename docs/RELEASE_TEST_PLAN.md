# Manual Windows release test plan

Use a clean disposable Windows amd64 VM with a snapshot. Record the Windows
build, SonDNS version/EXE SHA-256, source ZIP/manifest hashes and signing status.
Create a standard user and a separate administrator. Keep test DNS traffic
synthetic. A basic lifecycle pass was recorded on a disposable GitHub-hosted
Windows Server 2022 amd64 VM at commit `2ba849dec732`:
[run 39116932464](https://github.com/soncms/DoH/actions/runs/39116932464).
It covered service/driver installation and cleanup, local UDP/TCP DNS, transparent
IPv4 UDP/53, custom config/tray bindings and read-only GUI startup. It did not
cover every manual case below. A result applies only to its recorded EXE hash
and source commit; use the report for each subsequent run.
The optional [prebuilt test kit](CLEAN_WINDOWS_TESTS.md) prepares a deterministic
basic lifecycle run without Go or public resolvers; it does not replace the
manual cases below or establish a pass before being run in that VM.

The current harness also includes a query held at its encrypted upstream during
service stop, a quiet interval without harness DNS traffic, native shutdown-log
checks, and embedded DPI process startup/forced exit/restart/shutdown. These are
test procedures until a non-skipped pass is recorded for the changed commit.
The quiet interval does not exclude Windows background traffic. Process
lifecycle checks do not establish DPI effectiveness or application compatibility.

The harness additionally runs production migration hooks against a running
service: a real SCM startup failure while the candidate image is exclusively
locked must roll back and restore DNS, followed by a successful migration.
It checks settings/SCM/tray snapshots, backup hashes and helper cleanup. The built
GUI is also operated through its native menu/dialogs: default No, cancellation,
a real pre-stage config lock failure, then successful migration with imported
controls and a concrete backup path. Each procedure still needs an explicit
pass on the tested commit. This covers an already elevated session; UAC, power
loss and the other failure stages below remain separate cases.

The account fixture uses two newly created local Windows identities and actual
logon tokens to check standard-user monitoring/denied management and a separate
elevated administrator's authorized IPC log clear. Its live windowless child
also checks the native process handoff wait. It removes the owned accounts and
child and records `accounts.json`; no existing account or UAC policy is changed.
A pass proves those token/ACL/backend operations, not the credential prompt,
secure desktop, Manage-button handoff or cross-account GUI/tray startup.
The standard account uses an interactive logon; the administrator uses a batch
logon. The fixture does not modify UAC or logon-right policies.
It reads native service state through the production SCM query and log rows
through IPC `snapshot`; it does not require console-only IPC `state` support.
The standard-account process must initially deny a different admin's wait.
Fixture synchronization uses four anonymous event handles explicitly duplicated
with SYNCHRONIZE/EVENT_MODIFY_STATE into the suspended child, then recorded in a
SID-readable manifest before resume. No process rights or caller handles are
inherited, and named-event security is outside this fixture's scope.
After the production preparation helper grants only SYNCHRONIZE, a native wait
must succeed while process control access remains denied. The child also
verifies exact DACL restoration; actual UAC cancellation still needs review.

The IPv6 fixture additionally covers A/AAAA over UDP/IPv6 to a documentation
address and scoped link-local address, with timeout controls before/after the
service and removal of its temporary network entries. A non-skipped pass and
network cleanup report are required. It does not establish public IPv6 encrypted
upstream reachability, extension-header behavior, MTU handling or other adapters.

## Single EXE and elevation

- Start with only SonDNS.exe in a folder containing spaces. Open from a different
  working directory. Confirm the native GUI/tray, embedded notices/runtime and
  local data paths. Inspect file activity for application-owned profile writes.
- From the standard user, confirm read-only controls. Cancel Manage… UAC and
  verify the original window stays usable. Approve UAC with the other admin;
  verify one GUI instance remains and commands apply to the intended copy.
- From an unsafe folder, confirm Install offers a protected copy. Cancel, then
  complete the copy flow. Confirm the new ACLs, preserved custom upstreams and
  rejection of an existing destination. Check ordinary-user read and write denial.

## Service and driver

- With no SonDNS/WinDivert service present, install/start from the protected copy.
  Verify SCM binary/config paths remain in that folder and the driver signature
  is valid. Test controlled IPv4/IPv6 UDP/53 queries through a test upstream.
- Exercise stop/start/restart, custom upstream changes and DPI enabled/disabled.
  Confirm the selected upstream stays saved and no orphan helper remains.
- Stop after an idle period with no DNS requests. Confirm shutdown completes
  without a wake packet to a local/public resolver, and inspect logs for native
  shutdown/close errors, worker timeouts and incomplete driver cleanup. Repeat
  with an in-flight DNS request; the handle must close after its workers exit.
- Toggle Start with Windows, reboot and log in as the standard user. Confirm
  service/tray startup and local status access. Disabling must remove only the
  matching Run value and retain unrelated registry/service configuration.
- Install with a custom portable config path containing spaces. Verify the
  implicit GUI, UAC handoff, tray startup and SCM runtime select that same file.
  An explicit different config must not save settings or mutate the service.
- Inject a config-save failure. Confirm startup rollback or an explicit rollback
  failure, with the real SCM state shown in the GUI. Check delayed startup and
  dependencies, including a concurrent administrator change.
- Uninstall from the owning EXE. Verify the owned service/driver registrations
  are removed, data/runtime/backups remain, and another WinDivert user's service
  is not removed. Stop/uninstall must remain usable after unsafe ACL changes.

## Migration and recovery

The preview includes an explicit **Migrate existing service…** action. Normal
commands must still refuse a service in another folder. The transaction tests
and stopped SCM fixture alone do not establish a live migration. The disposable
lifecycle report supplies the running-service and locked-image rollback cases
only after a non-skipped pass; record the remaining cases below:

- Back up old config/startup/SCM binding and preserve old EXE/runtime before any
  change. Show source/destination and expected interruption before confirmation.
- Preserve custom upstreams, DPI choices and startup mode; redirect application
  logs into the new portable folder. Never delete/take over the old folder.
- Inject failures at copy, validation, stop, rebind, start and tray registration.
  Restore the previous binding/startup/config on failure, or provide a concrete
  recovery path if an external change prevents safe rollback.
- Confirm the new service starts only after validation and the old installation
  remains recoverable. Reboot and verify the intended service/tray path.
- Cancel confirmation and verify zero service/tray/config mutations. Test a
  stopped service, custom DNS upstreams, delayed startup and both tray value types.
- With an old folder under Desktop, verify the pre-confirmation warning and
  refusal to restart it as SYSTEM on rollback. Check manual recovery instructions.
- Interrupt the app/power between journal stages. Inspect SCM/files before manual
  recovery; no automatic recovery should run. Reject custom helper arguments,
  junction/hardlink sources and backup size/count limits without interrupting DNS.

## Evidence and publication

Save the exact steps, screenshots, source/artifact hashes and resulting SCM,
registry, process and filesystem observations. Mark every untested or failed
case explicitly. A stopped-SCM fixture or unit tests do not establish these
end-to-end results. Complete RELEASING.md, including signing, security reporting
and source/license review, before publishing binaries.
