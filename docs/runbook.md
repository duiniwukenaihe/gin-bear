# Production Runbook

## Release Checklist

1. Review configuration from `application-prod.yaml.example`.
2. Run the same pinned local verification command required by the README and
   security policy:

   ```bash
   GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 make verify
   ```
3. Before an RC or tag, run the complete audited gate with an explicit shuffle
   seed. The default is offline and therefore requires preinstalled tools, a
   populated module cache, and a local govulncheck database with a trusted
   SHA-256 manifest:

   ```bash
   SHUFFLE_SEED=20260711 STATICCHECK_BIN=/opt/gin-bear/bin/staticcheck STATICCHECK_EXPECTED_SHA256=<trusted-staticcheck-sha256> GOVULNCHECK_BIN=/opt/gin-bear/bin/govulncheck GOVULNCHECK_EXPECTED_SHA256=<trusted-govulncheck-sha256> GOVULNCHECK_DB=file:///opt/gin-bear/vulndb GOVULNCHECK_DB_MANIFEST=/opt/gin-bear/vulndb.manifest.sha256 GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256=<trusted-manifest-sha256> APIDIFF_BIN=/opt/gin-bear/bin/apidiff APIDIFF_EXPECTED_SHA256=84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f make verify-rc
   ```
4. Build the application binary with `VERSION`, `COMMIT`, and `BUILD_TIME` linker flags.
5. Confirm `/live`, `/ready`, `/version`, and `/metrics` in the target environment.
6. Confirm `server.shutdown_timeout`, `health.readiness_timeout`, `log.level`, and `redis.required` match the service's dependency profile.
7. Confirm the intended `framework.strict` and `framework.response_mode`
   values. Existing applications default to compatibility mode and raw
   responses; strict mode and envelope responses require separate migration.
8. Confirm production startup uses `IgniteE`, the process entry point owns its
   signal context for `Serve`, and no second serving owner can start.
9. Confirm each `CasbinFairing` receives a `CasbinEnforcer` from the current
   Bear container, JWT inputs over 16 KiB are rejected, and WebSocket origin,
   message, timeout, and connection boundaries match the production guide.
10. Confirm `main` CI passes, then create an annotated,
    immutable semantic-version tag on the exact reviewed `main` commit.
11. After the tag workflow completes, verify the GitHub Release, its generated
    notes, and GitHub-generated source archives. Confirm the module and CLI are
    available through the Go toolchain:

   ```bash
   GOBIN=$(mktemp -d) go install github.com/duiniwukenaihe/gin-bear/cmd/bear@<version>
   ```

The release workflow runs only for `v*` tags and has one responsibility:
create the GitHub Release with generated notes. GitHub supplies the standard
source archives automatically. Framework quality checks run before tagging
and in the `main` CI workflow; the tag workflow does not repeat them. The
release job grants `contents: write` only for publishing. The pushed release
tag must be annotated and target the exact reviewed `main` commit. After
publishing, the workflow requests the matching version from `proxy.golang.org`
so the Go Module is indexed. A CLI
installed with `go install ...@version` reads that module version from Go build
information and uses it as the generated project's framework dependency;
local development builds still require `--framework-version` explicitly.

The full `verify-rc` command remains available for an explicit pre-tag audit.
A local release operator with an isolated trusted `GNUPGHOME` can run:

```bash
RC_BASE_REF=origin/main RC_RELEASE_TAG=<version> RC_EXPECTED_VERSION=<version> RC_VERIFY_TAG_SIGNATURE=true RC_TRUSTED_KEYRING=/opt/gin-bear/release-gnupg SHUFFLE_SEED=20260711 STATICCHECK_BIN=/opt/gin-bear/bin/staticcheck STATICCHECK_EXPECTED_SHA256=<trusted-staticcheck-sha256> GOVULNCHECK_BIN=/opt/gin-bear/bin/govulncheck GOVULNCHECK_EXPECTED_SHA256=<trusted-govulncheck-sha256> GOVULNCHECK_DB=file:///opt/gin-bear/vulndb GOVULNCHECK_DB_MANIFEST=/opt/gin-bear/vulndb.manifest.sha256 GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256=<trusted-manifest-sha256> APIDIFF_BIN=/opt/gin-bear/bin/apidiff APIDIFF_EXPECTED_SHA256=84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f make verify-rc
```

When `RC_RELEASE_TAG` is non-empty, `RC_VERIFY_TAG_SIGNATURE` is mandatory and
must be exactly `true` or `false`. `true` also requires
`RC_TRUSTED_KEYRING` to name an isolated `GNUPGHOME`; verification must emit
both `VALIDSIG` and `TRUST_FULLY` or `TRUST_ULTIMATE`. `false` is recorded as an
explicit exemption. Supplying the signature variable without a release tag is
invalid.

The ordinary CI quality job installs pinned versions under
`${RUNNER_TEMP}/bin` and passes all three absolute binary paths to `make verify`,
so the gate does not depend on tools preinstalled by the runner image.
Offline verification rejects remote vulnerability database URLs. It accepts
only an absolute local directory or `file://` URI, requires an independently
trusted SHA-256 manifest, verifies every relative manifest entry inside the
database root, and records the canonical database and manifest identities.

The RC path always enforces total coverage `70.0` and every critical group
`80.0`; lower caller-provided environment values do not reduce these gates.

## v0.9.2 Release Audit

`v0.9.2` is the next published release after `v0.9.1`. The historical
diagnostics below are not formal release gate evidence. Publication requires
the complete gate to pass against the exact clean commit referenced by the
annotated release tag. The source-only release workflow publishes that already
reviewed tag without repeating the quality gate.
All commands use `GOSUMDB=sum.golang.org` and `GOTOOLCHAIN=go1.25.12`, run in
the foreground, and complete before the next command starts.

The reproducible coverage sequence keeps its profile outside the worktree:

```bash
profile=$(mktemp /tmp/gin-bear-coverage.XXXXXX)
trap 'rm -f "$profile"' EXIT
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go test ./... -coverprofile="$profile" -count=1
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 scripts/check-coverage.sh "$profile"
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 go tool cover -func="$profile"
```

The development-time profile regenerated on 2026-07-11 contained 2,822 covered
statements out of 3,723, or 75.799087% before display formatting (75.8%). These
figures are diagnostics, not current-commit release evidence. The checked-in
manifest lists every production file in each critical group; handler includes
all of `bear.go`, `handler.go`, `responder.go`, and `fairing.go`; lifecycle
includes all of `bear.go` and `lifecycle.go`; and scaffold is checked against the current
platform's complete `internal/scaffold` production `GoFiles`. The gate reports:

```text
critical coverage handler 82.9%
critical coverage binding 88.5%
critical coverage errors 94.5%
critical coverage config-loader 91.8%
critical coverage lifecycle 84.9%
critical coverage auth 92.2%
critical coverage migration-lock 80.3%
critical coverage cron-lock 88.9%
critical coverage cli 93.1%
critical coverage scaffold 82.9%
```

Public API compatibility is checked without repository history or local tags:

```bash
APIDIFF_BIN=/opt/gin-bear/bin/apidiff APIDIFF_EXPECTED_SHA256=84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f scripts/check-api-compat.sh
```

The committed `scripts/api/v0.9.1.txt` module manifest covers every public Go
package from v0.9.1. The pinned official `apidiff` gate permits additions and
rejects removals or incompatible changes; the separate v0.9 consumer fixture is
also compiled by `go test ./...`. The gate first checks the committed SHA-256
sidecar. The default path requires the controlled `APIDIFF_BIN` and never trusts
an arbitrary PATH binary or invokes `go run module@version`; set
`API_COMPAT_ALLOW_NETWORK=1` to opt into the pinned fallback. Set
`API_BASELINE_REBUILD=1` to rebuild the manifest from the public
`v0.9.1` Go module cache and compare it byte for byte; this path does not use a
local tag, clone, or shallow repository history. Reconstruction is an explicit
manual or independent audit and is not required by the default offline gate,
which checks only the committed hash and additive API compatibility. The release
workflow prepares the pinned binary before invoking that offline gate.
`APIDIFF_EXPECTED_SHA256` is mandatory with `APIDIFF_BIN`; it must be an
explicit trusted digest supplied by the operator or CI, never a digest computed
and trusted by the same gate invocation. The gate rejects a hash mismatch,
unreadable Go build info, or a build path, module, pseudo-version, or commit that
does not match the pinned `golang.org/x/exp/cmd/apidiff` identity. The network
switch, rebuild switch, tool source, canonical executable path, actual and
expected SHA-256, raw `go version -m` output, and parsed actual and expected
build identity are printed to stdout and appended to `API_COMPAT_METADATA`
when that path is provided. Metadata is evidence only and never bypasses these
checks. `APIDIFF_BIN` must be a non-empty absolute path to a regular,
non-symlink executable.

`scripts/release-check.sh` independently prints and persists
`release_check_network` and `release_check_network_opt_in`. Set
`RELEASE_CHECK_METADATA` to retain the evidence at a chosen path; when it is
unset, the script creates a retained temporary metadata file and prints its
location. `verify-rc` appends this evidence to its artifact metadata rather
than relying on the outer gate's network fields alone.

Remote branch hygiene is also offline by default. Set `RC_REMOTE_HYGIENE=1` to
opt into `git ls-remote --heads origin`. `RC_ALLOW_NETWORK=1` separately opts
the full RC gate into an online Go proxy, tool fallback, and vulnerability DB;
the default forces `GOPROXY=off`, `GOSUMDB=off`, and `GOTOOLCHAIN=local`.
Offline mode does not promise to populate an empty `GOMODCACHE`. All switches
accept only `0` or `1`. When `SHUFFLE_SEED` is omitted, `verify-rc` derives a
stable seed from the candidate commit and tree. The 20-run shuffle stage uses
`RC_SHUFFLE_TIMEOUT=90m` by default so command-heavy packages can complete all
iterations. Override it only with a positive integer followed by `s`, `m`, or
`h`; the effective timeout is recorded in RC metadata.

Run the release-only compatibility test once with:

```bash
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12 BEAR_RELEASE_E2E=1 go test ./scripts/releasee2e -run '^TestReleaseCandidateApplications$' -count=1 -v
```

It builds a v0.9-style application and a newly generated application in
temporary directories. Each fixture must start, answer `/live`, `/ready`, a
successful route, a validation failure, and an unauthorized request, then exit
cleanly after `SIGTERM`. Captured logs and stdout traces are rejected if they
contain the request secret.

After coverage and compatibility pass, commit the reviewed fixes and run the
final gate through its tracked entry point from a clean HEAD. It records the
actual commit and git tree hash, the resolved `RC_BASE_REF` merge base, Go and
pinned tool versions, shuffle seed `20260711`, shuffle timeout, each command
and exit code, and clean before/after worktree status:

```bash
SHUFFLE_SEED=20260711 STATICCHECK_BIN=/opt/gin-bear/bin/staticcheck STATICCHECK_EXPECTED_SHA256=<trusted-staticcheck-sha256> GOVULNCHECK_BIN=/opt/gin-bear/bin/govulncheck GOVULNCHECK_EXPECTED_SHA256=<trusted-govulncheck-sha256> GOVULNCHECK_DB=file:///opt/gin-bear/vulndb GOVULNCHECK_DB_MANIFEST=/opt/gin-bear/vulndb.manifest.sha256 GOVULNCHECK_DB_MANIFEST_EXPECTED_SHA256=<trusted-manifest-sha256> APIDIFF_BIN=/opt/gin-bear/bin/apidiff APIDIFF_EXPECTED_SHA256=84b7e058a4df23bc0e21d3eae07dedc0b93cee85b40ee8c65701944eed5f742f make verify-rc
```

By default logs are retained in a `mktemp` directory outside the repository;
CI sets `RC_ARTIFACT_DIR` under `runner.temp` and uploads it. The final hygiene
step permits only local `main`, `codex/production-baseline`, and
`codex/v09x-framework-hardening` branches, including in detached-HEAD CI. It
limits remote heads to `main` and `codex/production-baseline`, rejects
container/Kubernetes/Helm files and `coverage.out` at any depth outside `.git`,
and requires both initial and final worktree status to be empty.

### Historical Development-Time Validation: 2026-07-11

The run associated with commit
`1db2743e3b1146ecc6592e0ea46cfa4e5ad311c1` was made from a dirty worktree
while review fixes were still under development. It is development-time
validation only and is not evidence for the current commit or a formal release
candidate. Its retained diagnostics used shuffle seed `20260711`, Go 1.25.12,
staticcheck 2026.1 (`v0.7.0`), and govulncheck `v1.6.0`; the audit directory was
`/var/folders/n0/1dxtrxzn305_fjv9vgth7pf40000gn/T/gin-bear-rc.H44w3t`.
`results.tsv` recorded exit code 0 for `clean`, `count1`, `shuffle20`, `race3`,
`vet`, `staticcheck`, `govulncheck`, `release-check`, `diff-check`, `hygiene`,
and `final`.

Govulncheck reported zero reachable vulnerabilities. It also reported two
vulnerabilities in imported packages and twenty in required modules that this
code does not call. The macOS race link printed `malformed LC_DYSYMTAB`
warnings, but every affected command exited 0. The before/after status snapshots
were byte-identical, the release-owned coverage profile was removed, and no
worktree `coverage.out` remained. Remote heads were only `main` and
`codex/production-baseline`; the active local development branch remained
allowed. These historical results must not be reused as fresh gate evidence.
Formal release gate evidence is created only by a complete `make verify-rc`
run after the fixes are committed, the starting HEAD is clean, and the
annotated release tag targets that exact commit.

## Rollback

1. Stop routing new traffic to the unhealthy version.
2. Roll back to the previous application binary or release artifact.
3. Check `/ready` before restoring traffic.
4. Review `/version` to confirm the running commit.
5. For framework behavior changes, follow the exact configuration and rollback
   sequence in [the v0.9.1 to v0.9.2 migration guide](migration-v0.9.1-to-v0.9.2.md).

## Migration Recovery

Run migrations as a separate deploy step before starting the new app version. If a migration job is interrupted while holding the migration lock, verify no migration process is still running, then call `MigrationRunner.ForceUnlock(ctx)` from an admin command before retrying.

Use `MigrationRunner.Down(ctx, migrations, steps)` only for reviewed rollback SQL. Prefer forward fixes when data loss is possible.

## Observability

Use `/metrics` for Prometheus scraping and `EnableTracing(ctx)` with OTLP for distributed tracing. Request logs include request id and latency information. During incidents, correlate `/version`, trace ids, request ids, and deployment timestamps.

For deeper framework diagnostics, temporarily raise `LOG_LEVEL=debug` and restore it after the incident.

## Incident Response

1. Identify blast radius from health checks, metrics, traces, and logs.
2. Decide whether to roll back, fail over, or disable traffic.
3. Preserve logs, trace ids, release metadata, and migration history.
4. File follow-up work for missing alerts, missing dashboards, or unclear runbook steps.
