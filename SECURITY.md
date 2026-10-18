# Security policy

SonDNS currently has local previews and no supported stable release. Fixes will
target current source and the latest public release when one exists. No response
or fix-time service level is promised.

## Report privately

The intended public-project channel is GitHub Private Vulnerability Reporting:
[report a vulnerability](https://github.com/soncms/DoH/security/advisories/new).
That feature must be enabled and checked before publication; it is not currently
an established channel for this private preview. Do not post exploit details,
credentials or sensitive logs in public issues. If the reporting button is
unavailable, contact the maintainer privately through a previously established
channel before sending sensitive details.

Include versions/EXE hashes, Windows version, prerequisites, reproduction and
impact. Redact unrelated DNS history and credentials. Test only on machines and
networks you are authorized to assess.

## Security boundaries

- The SYSTEM service uses a kernel packet driver. Install trusted builds in a
  protected folder; folder/ancestor ACL checks prevent ordinary-user replacement
  of elevated executable/config inputs.
- A normal GUI reads status; mutations require UAC. Mutating elevated IPC calls
  require an administrator/SYSTEM client token.
- Authenticated local users can read live DNS history. SonDNS assumes a trusted
  Windows machine, not history isolation between untrusted local accounts.
- Embedded hashes detect altered assets, not the publisher of an unsigned EXE.
  Check release signatures, checksums and source provenance when available.
- Startup rollback checks configuration and uses narrow SCM writes. SCM has no
  compare-and-swap, so another administrator can still race the final write to
  the same startup field.

DNS transport encryption does not hide requests from the resolver. See
[PRIVACY.md](PRIVACY.md). SonDNS does not protect against a compromised admin,
kernel, operating system or upstream resolver.

See [RELEASING.md](RELEASING.md) for validation and remaining clean-machine checks.
Windows CI also runs a [known Go vulnerability gate](docs/DEPENDENCY_SECURITY.md).
It retains module-only matches and local replacement provenance, and checks
canonical upstream baselines for local replacements through OSV. A baseline
match blocks pending source review without proving the patched code exploitable.
A pass is scoped to the analyzed Go source graph and database snapshots.
Do not disable Windows security software to run a preview or treat passing unit
tests as a completed kernel-driver security review.
