# Security Policy

## Supported Versions

Security fixes are applied to the active default branch and current production hardening branch. Generated applications should update from the scaffold regularly and run `scripts/release-check.sh` before release.

## Reporting a Vulnerability

Please report suspected vulnerabilities privately to the repository maintainers instead of opening a public issue. Include affected version or commit, reproduction steps, impact, and any known workaround.

## Security Updates

Security fixes should include tests when practical, a clear changelog or commit message, and a fresh `govulncheck ./...` result. High-impact dependency updates should also pass Docker build in CI.
