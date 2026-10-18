# SonDNS local DNSCrypt changes

Base: `github.com/AdguardTeam/dnscrypt` v0.0.3, copied from the Go module cache
verified using its `go.sum` entry. `upstream-files.json` records the original
upstream bytes; `client.go` is the only changed upstream file. The original
Apache-2.0 license and complete module source/tests are retained.

`Client.ExchangeContext` closes its own socket when the request context is
canceled. Certificate fetching uses an explicitly owned DNS connection with
the same cancellation behavior. This interrupts blocked UDP/TCP reads instead
of retaining an interceptor worker until its old I/O deadline. It does not
change encryption, certificate signature verification, stamp pins or trust.
`ExchangeConnContext` continues to leave the caller-supplied socket under its
caller's control.

SonDNS's root regression tests cover UDP/TCP encrypted exchanges and certificate
fetch cancellation with loopback fixtures; no public resolver is used. The
disposable Windows lifecycle harness additionally holds an encrypted request
upstream while stopping the real service and WinDivert.
