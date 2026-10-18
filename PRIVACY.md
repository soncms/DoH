# Privacy and network behavior

SonDNS sends DNS requests to the selected upstream. That provider receives query
names, record types and the connection's source address, and controls its own
logging/filtering/retention. Encryption does not hide queries from the resolver
or provide VPN/anonymity protection.

## Presets

- **SonCMS**: `https://dns.soncms.net/dns-query/sondns`.
- **NextDNS**: `https://dns.nextdns.io/2e2e3c`, a shared configured profile. Its
  operator controls filtering and any profile-level logging settings.

These are the shipped presets, not assertions of provider retention policies.
Use providers/profile IDs you control or trust. Custom saved upstreams survive
DPI/service changes; explicitly choosing a preset replaces the selection.
Hostname bootstrap can generate address-resolution traffic according to the
transport configuration.

## Local data

The request table keeps at most 200 entries in memory: domains, results/addresses
and latency. Clear removes that memory history, not provider-side records. There
is no separate DNS-history file and no raw-packet/client-IP storage in the table.
Authenticated local users can read it through the control pipe.

Configuration and diagnostic/helper logs stay beside the EXE under `data/`.
Errors/helper output may contain domains, endpoints, arguments or local paths;
review/redact logs before sharing. Protect configs and backups containing saved
upstreams and profile IDs.

Explicit service migration creates `backups/migration-<unique>/` beside the EXE.
It preserves legacy config/EXE/runtime, SCM/tray settings and any destination
config, with hashes and recovery reports. These copies can include credentials
or custom endpoints; they are never uploaded automatically. Review and remove
them deliberately when recovery is no longer needed.

SonDNS does not implement analytics or automatic crash uploads. Windows/security
software may separately record process, driver or network activity. Optional
DPI processing uses the local helper; it is not an encrypted VPN tunnel.

Uninstall preserves portable configuration, logs and runtime. Delete the folder
after uninstall if you also want those files removed. Local deletion does not
affect an upstream provider's retained records.
