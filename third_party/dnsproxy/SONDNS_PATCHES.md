# SonDNS local dnsproxy changes

Base: `github.com/AdguardTeam/dnsproxy` v0.86.0, copied from the Go module cache
verified using its `go.sum` entry. `upstream-files.json` records the original
upstream bytes. The original Apache-2.0 license and module source/tests are kept.

The only changed upstream file is `upstream/dnscrypt.go`: DNSCrypt TCP fallback
after a truncated UDP response retains the caller's context rather than using
`context.WithoutCancel`. Its timeout can shorten, but cannot extend, the caller's
deadline. This lets service shutdown interrupt TCP fallback as well as the first
attempt. The DNSCrypt module's socket-cancellation patch is documented separately
in `third_party/dnscrypt/SONDNS_PATCHES.md`. No certificate checks are bypassed.

SonDNS's root regression test forces encrypted UDP truncation, holds the TCP
fallback at its loopback upstream, then cancels the caller before releasing it.
