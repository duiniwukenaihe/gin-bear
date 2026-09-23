# Release process

`main` is the only framework release line. Feature branches and release
candidates are review and verification workspaces, not permanent release
branches. The current published root module is `v0.9.4`; a version name is not
published until its annotated tag and Release workflow complete. This update
is broader than a typical patch: opt-in features,
production behavior fixes, and a newer Go toolchain are all listed in the
changelog and upgrade guide. It may use the `v0.9.4` name only after the
`v0.9.3` public API comparison and application upgrade checks pass.

For ordinary work, branch from the latest remote `main`: use `feat/<topic>`
for a feature, `fix/<topic>` for a repair, or `codex/<topic>` for agent work.
Keep branches short-lived and merge reviewed changes through a pull request.
Protect GitHub `main` with required `CI / verify`, `CI / race`, and
`Integration / integration` checks, plus review; do not force-push it. There
is no permanent `develop` or `release/*` branch. Keep compatible maintenance
and opt-in additions within `v0.9.x` while their upgrade path remains tested;
reserve a minor-version change for a deliberately reviewed later phase. An
`-rc.N` suffix identifies an explicitly selected trial version, not a stable
production release. The version number never replaces a changelog or a
compatibility check.

## What a tag publishes

The root Go module uses `v0.9.4` for the final version. An `-rc.N` tag is
optional when a trial release is needed. The `Release` workflow runs on a
pushed root `v*` tag. It
requires an annotated tag whose commit belongs to `main`, reruns `make verify`
and the PostgreSQL, MySQL, and Redis integration suite, then creates the
GitHub Release. Candidate tags are marked as prereleases and not Latest.
Pushing a tag is therefore a public release action, not a way to start private
verification. Do not move or reuse a published tag.

`v0.9.4` is a recorded exception: its initial tag workflow failed before
quality checks, and a later run passed tag and integration checks but exposed
a development-only generator test assertion in the tagged build. Its GitHub
Release was published manually from the unchanged annotated tag. The test is
fixed after `v0.9.4`; the historical tag run remains failed. Future releases
must pass the complete tag workflow before being announced.

If the tag-triggered workflow fails, fix the workflow on `main` through a PR,
then manually run `Release` from `main` with the existing tag as input. The
retry checks out and verifies that exact tag, reruns quality and integration,
and leaves an already-created GitHub Release unchanged. Keep the failed run
for audit; never delete or move the published tag to trigger another run.

This is a source-only release: GitHub supplies source archives and the Go
module proxy serves the tagged module. No compiled binary is checked into Git
or attached by this workflow. Users install the matching generator with
`go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.4` and pin the
framework with `go get github.com/duiniwukenaihe/gin-bear@v0.9.4` **after**
that tag exists. The released generator uses its own version for generated
projects; do not mix an older generator with newer templates.

`extensions/agent` and `tools/bear-mcp` are separate Go modules. A root
`v0.9.4` tag does **not** publish versions of either nested module. They need
independent, directory-prefixed tags such as `extensions/agent/v0.1.0` and
`tools/bear-mcp/v0.1.0` after their own consumer checks. Until then, describe
them as experimental source components, not installable versioned releases.
The Agent module currently requires the published root `v0.9.3` for external
module resolution; a consumer may explicitly select a newer root version.

## From candidate to final release

1. Reconcile the candidate with the user's current working tree without
   discarding uncommitted changes. Review the diff, merge the accepted work
   into GitHub `main` through a pull request, and require a clean, fixed
   commit for all later checks. If the PR merge creates a new commit, rerun
   commit-bound release checks on that resulting `main` commit.
2. Review [CHANGELOG.md](../CHANGELOG.md) and the
   [upgrade guide](upgrade-v0.9.3-to-v0.9.4.md). Keep new work under
   `Unreleased`. Before the final tag, move the reviewed entries into a dated
   `## [v0.9.4]` section and start a new `Unreleased` section. Update README,
   production, support, and security version references in that same commit.
3. On the exact candidate commit, run `make verify` and
   `BEAR_INTEGRATION_REQUIRE=pg,mysql,redis scripts/test-integration.sh` with
   real services. The 20-run shuffle and three-run race stages in
   `make verify-rc` are optional stress checks, not a release gate. Confirm an
   external consumer can resolve
   the root module and the Agent module without relying on their own local
   `replace` directives. Compare the candidate public API against published
   `v0.9.3`, not only the repository's older `v0.9.1` baseline. Scan the
   candidate tree and reachable Git history for credentials; inspect scanner
   findings before publishing. `agent.md` and `AGENTS.md` must remain ignored
   and untracked.
4. Require the `main` CI checks to pass on that commit. If broader trial is
   needed, create an annotated `v0.9.4-rc.1` tag on that `main` commit and
   push it. Wait for the tag workflow and Go proxy publication before asking
   a trial application to pin `@v0.9.4-rc.1`. Fixes go to `main`; a second
   candidate gets a new tag such as `v0.9.4-rc.2`.
5. Once the candidate has no release blockers and the dated changelog is in
   the commit, rerun the gates on that final `main` commit. Create and push an
   annotated `v0.9.4` tag pointing at it. The tag workflow must finish before
   announcing the version. Verify the GitHub Release, its source archives,
   and the Go proxy module
   version, then install the released generator in a separate application and
   confirm the generated project builds and starts without a local `replace`.

Do not merge a candidate by changing `main`'s ref beneath a dirty checkout.
Do not call a green local unit test, a candidate branch, or a pushed tag alone
a finished release. If verification fails after a tag, stop promotion, fix on
`main`, and use a new tag; never force-push a replacement tag. Existing
applications can roll back by pinning `v0.9.3` and redeploying their prior
build. Database migrations and policy data require a separate, reviewed
rollback decision.
