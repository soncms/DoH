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
New configurations select SonCMS alone. With multiple configured upstreams,
SonDNS sends queries in parallel, so more than one provider can receive the
same query. The example JSON includes both presets to demonstrate that setup.

## Bootstrap and interception

An encrypted upstream's hostname may need an address lookup before SonDNS can
connect. Explicit bootstrap IPs or known provider addresses avoid that lookup.
Otherwise, SonDNS queries the configured bootstrap servers for A/AAAA records
over ordinary UDP/TCP DNS. The default bootstrap servers are Cloudflare's
`1.1.1.1`, `1.0.0.1`, `2606:4700:4700::1111` and `2606:4700:4700::1001`, on port 53.
If those attempts fail and time remains, it tries the Windows system resolver.
These lookups expose the upstream hostname to the bootstrap/system resolver
and can be visible to the network even when subsequent DNS transport is encrypted.

Transparent interception captures outbound non-loopback UDP/53 on IPv4/IPv6.
TCP DNS, application-owned encrypted DNS and traffic outside that filter follow
their existing paths. Unsupported captured packets, including fragments and
authenticated IPv6 packets, are reinjected unchanged. Their original resolver
and network policy apply. SonDNS does not enforce encryption for every DNS path.

The native portable GUI uses embedded runtime files. Legacy console builds
also offer component check/install commands: invoking them contacts GitHub's
API/release/raw-content services and can download runtime binaries. Console
DNS diagnostics can send synthetic `example.com` queries to configured resolvers.

## Local data

The request table keeps at most 200 entries in memory: domains, results/addresses
and latency. Clear removes that memory history, not diagnostic files or
provider-side records. There is no separate DNS-history file and no
raw-packet/client-IP storage in the table.
Authenticated local users can read it through the control pipe.

Configuration and diagnostic/helper logs stay beside the EXE, normally under
`data/`. A custom relative diagnostic-log path can select another location
within the portable folder; an old absolute path is redirected to `data/sondns.log`.
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
