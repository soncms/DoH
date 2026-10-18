# Pinned native source/reference archives

These four archives mirror the exact upstream bytes already identified in
`assets/native-source-lock.json`. They are included so source packaging does not
depend on the historical Cygwin mirror being available on a build runner.

`scripts/package-source.ps1` uses this directory as its default native cache.
Collection verifies SHA-256, shipped DLL/driver correspondence and the Cygwin
upstream source tree before accepting any archive. This cache does not replace
those checks. Provenance, original download URLs, license terms and verification
limits remain in the lock file, `RUNTIME_PROVENANCE.md` and the source package.

The source ZIP includes these archives once, under `native-sources/`, together
with the verification receipt. No cached archive is used as an executable test
dependency or loaded as a driver during source collection.
