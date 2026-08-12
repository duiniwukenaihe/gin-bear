# Security Policy

## Supported Versions

`v0.9.2` is the current supported release. Users upgrading from `v0.9.1`
should follow the
[v0.9.1 to v0.9.2 migration guide](docs/migration-v0.9.1-to-v0.9.2.md).

Generated applications should update from the scaffold regularly and run
`GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify` before release.
Published CLI archives include SHA-256 checksums and release metadata so a
deployment can be traced to its source commit and verified before use.

## Reporting a Vulnerability

Please report suspected vulnerabilities privately to the repository maintainers instead of opening a public issue. Include affected version or commit, reproduction steps, impact, and any known workaround.

## Security Updates

Security fixes should include tests when practical, a clear changelog or commit message, and a fresh `govulncheck ./...` result.
