# SonDNS portable

Double-click **SonDNS.exe** to open the native Windows window and tray icon.
There is no CMD menu. DNS resolution runs inside SonDNS; you do not need to
download or install a separate dnsproxy executable.

Keep the EXE in a dedicated folder on a local disk, for example `F:\SonDNS`.
The application stores its files in that folder:

```text
SonDNS/
  SonDNS.exe
  LICENSE / NOTICE / THIRD_PARTY_NOTICES.md
  licenses/                 included component copyright/license texts
  data/
    sondns.json
    sondns.log
    helper/                 temporary/home files for the DPI helper
  runtime/
    embedded/
      WinDivert.dll
      WinDivert64.sys
      winws2.exe
      cygwin1.dll
      zapret2/
        lua/
        windivert.filter/
```

WinDivert and DPI assets are embedded in the EXE and extracted locally when
needed. Application files are not stored in AppData, ProgramData or Program
Files. Relative paths are resolved from the EXE directory, including when the
Windows service starts with a different working directory. Old absolute log
paths are redirected to `data/sondns.log` in memory.

Custom upstreams in an existing local config are preserved when changing DPI
or installing the service. They appear as **Custom (saved configuration)** in
the DNS menu; selecting a preset explicitly replaces them.

Opening the window does not install, start or replace a service. A normal
window is read-only: use **Manage…** to reopen it through Windows UAC before
changing DNS/DPI, clearing the service log or managing the service. Cancelling
UAC leaves the original window open. Windows registers the service and driver with SCM; their
application binaries and data stay in the portable folder. Closing the window
hides it in the tray; **Exit** closes the interface while a running service
continues resolving DNS.

**Start with Windows** controls the machine-wide service startup and a matching
machine-wide tray entry for interactive users. It does not write a tray entry
into the administrator credential account's personal profile.
The tray command includes the selected portable config path. Opening the EXE
without an explicit config follows an owned service's config; an explicit
different config cannot change that service's DNS/DPI/startup settings. Stop
and uninstall remain available from the owning EXE for recovery. An overlong
Windows Run command is rejected with a request for a shorter folder/config path.

The SYSTEM service accepts only a folder whose owner and write permissions
are restricted to administrators/SYSTEM, including its existing files and
ancestor folders. Junctions, symlinks, hard-linked app files, network/removable
drives and SUBST aliases are rejected. The app also checks inherited rules for
files it will extract later. Stopping/uninstalling remains available to recover
an installation whose folder permissions have become unsafe.

When the current location is unsuitable, **Install** offers to create a new
portable copy. **Create portable copy…** is also available in the service menu.
Choose a trusted parent location, normally a fixed drive root such as `F:\`,
and confirm the new folder path. SonDNS creates that folder with its protected
permissions, copies the EXE and your settings, and opens the new copy. Existing
folders are never overwritten or recursively taken over. The current service
is left in place; normal commands refuse to modify a service bound elsewhere.
The original source/download folder remains available for you to remove later.

To switch a CMD/older installation from another folder, open the protected new
copy as administrator and choose **Migrate existing service…**. Review the source,
destination, interruption and recovery limits, then confirm. It imports saved
upstreams, DPI capture settings and actual service startup mode, redirects logs
locally, and uses the new EXE's embedded runtime. Custom legacy helper paths or
arguments require manual review and are rejected by this importer.

Before stopping DNS, migration flushes a bounded backup under
`backups/migration-<unique>/`: inert `.bin` copies of the old EXE/config/runtime,
file hashes, prior SCM/Run settings, any destination config, a journal and
`RECOVERY.txt`. Logs and historical backups are excluded. Successful migration
preserves whether the service was running or stopped. Service registration
changes only its binary command; account, dependencies, description and startup
details are retained. Tray startup follows the existing service startup choice.

Failures attempt guarded rollback. An unsafe old folder (commonly a folder under
Desktop) is never automatically restarted as SYSTEM; the old binding/settings
are restored and DNS can stay stopped. Concurrent administrator changes can
prevent rollback; the error identifies the backup/report. A crash/power loss
requires manual recovery. SCM/registry do not provide compare-and-swap, so these
checks cannot eliminate the final read/write race with another administrator.
No migration runs merely by opening the GUI. Review/remove backups deliberately;
they can contain custom endpoints or credentials. Clean-machine migration and
driver/UAC lifecycle testing remains required before release.

Use **Uninstall service** before moving an installed folder: SCM stores an
absolute EXE path. Uninstalling keeps the EXE, configuration, logs, runtime and
backups. Move the folder, then install from the new location. A different copy
of SonDNS refuses normal changes/removal; migration is a separate confirmed action.

The default DNS endpoint is `https://dns.soncms.net/dns-query/sondns`. The
NextDNS preset uses profile `2e2e3c`. These services receive DNS requests when
selected. Request history stays in memory (up to 200 requests); the runtime
diagnostic log is stored locally. Authenticated local users can read live DNS
history through IPC; use SonDNS on a trusted Windows account/machine. Mutating
an elevated service requires an administrator/SYSTEM token verified by the
server.

Build on Windows amd64 with Go 1.27.2 and PowerShell 7:

```powershell
./scripts/build.ps1 -Version portable-dev
```

The output is `dist/SonDNS-Portable/SonDNS.exe`, with build metadata and a
SHA-256 checksum alongside it. `-Sign` / `-Release` require a publisher's real
Authenticode certificate. An unsigned preview is for local verification;
public release additionally requires completing the third-party source and
license obligations described in `THIRD_PARTY_NOTICES.md`.
