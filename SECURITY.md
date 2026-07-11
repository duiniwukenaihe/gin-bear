# Security Policy

## Supported Versions

Security fixes are applied to the current `v0.10.x` release line and the active
default branch. `v0.9.1` is the final v0.9 maintenance release; users should
plan an upgrade with the [v0.9 to v0.10 migration guide](docs/migration-v0.9-to-v0.10.md).

Generated applications should update from the scaffold regularly and run
`GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify` before release.
Published CLI archives include SHA-256 checksums and release metadata so a
deployment can be traced to its source commit and verified before use.

## Reporting a Vulnerability

Please report suspected vulnerabilities privately to the repository maintainers instead of opening a public issue. Include affected version or commit, reproduction steps, impact, and any known workaround.

## Security Updates

Security fixes should include tests when practical, a clear changelog or commit message, and a fresh `govulncheck ./...` result.
