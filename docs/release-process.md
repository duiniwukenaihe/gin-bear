# Release process

`main` is the only framework release line. Feature branches and release
candidates are review and verification workspaces, not permanent release
branches. The current root module version is `v0.9.5`; a version is published
only after its annotated tag and Release workflow complete. This maintenance release
requires public API comparison against `v0.9.4` and application upgrade checks.

For ordinary work, branch from the latest remote `main`: use `feat/<topic>`
for a feature, `fix/<topic>` for a repair, or `codex/<topic>` for agent work.
Keep branches short-lived and squash reviewed release changes into one main
commit through a pull request.
Protect GitHub `main` with required `CI / verify`, `CI / race`, and
`Integration / integration` checks, plus review; do not force-push it. There
is no permanent `develop` or `release/*` branch. Keep compatible maintenance
and opt-in additions within `v0.9.x` while their upgrade path remains tested;
reserve a minor-version change for a deliberately reviewed later phase. An
`-rc.N` suffix identifies an explicitly selected trial version, not a stable
production release. The version number never replaces a changelog or a
compatibility check.

## What a tag publishes

The root Go module uses `v0.9.5` for the final version. An `-rc.N` tag is
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

Release notes include the matching bilingual changelog section plus generated
GitHub comparison notes. Update English and Chinese README and upgrade guidance
in the same release commit.

If the tag-triggered workflow fails, fix the workflow on `main` through a PR,
then manually run `Release` from `main` with the existing tag as input. The
retry checks out and verifies that exact tag, reruns quality and integration,
and leaves an already-created GitHub Release unchanged. Keep the failed run
for audit; never delete or move the published tag to trigger another run.

This is a source-only release: GitHub supplies source archives and the Go
module proxy serves the tagged module. No compiled binary is checked into Git
or attached by this workflow. Users install the matching generator with
`go install github.com/duiniwukenaihe/gin-bear/cmd/bear@v0.9.5` and pin the
framework with `go get github.com/duiniwukenaihe/gin-bear@v0.9.5` **after**
that tag exists. The released generator uses its own version for generated
projects; do not mix an older generator with newer templates.

`extensions/agent` and `tools/bear-mcp` are separate Go modules. A root
`v0.9.5` tag does **not** publish versions of either nested module. They need
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
   [upgrade guide](upgrade-v0.9.4-to-v0.9.5.md). Keep new work under
   `Unreleased`. Before the final tag, move the reviewed entries into a dated
   `## [v0.9.5]` section and start a new `Unreleased` section. Update README,
   production, support, and security version references in that same commit.
3. On the exact candidate commit, run `make verify` and
   `BEAR_INTEGRATION_REQUIRE=pg,mysql,redis scripts/test-integration.sh` with
   real services. The 20-run shuffle and three-run race stages in
   `make verify-rc` are optional stress checks, not a release gate. Confirm an
   external consumer can resolve
   the root module and the Agent module without relying on their own local
   `replace` directives. Compare the candidate public API against published
   `v0.9.4`, not only the repository's older `v0.9.1` baseline. Scan the
   candidate tree and reachable Git history for credentials; inspect scanner
   findings before publishing. `agent.md` and `AGENTS.md` must remain ignored
   and untracked.
4. Require the `main` CI checks to pass on that commit. If broader trial is
   needed, create an annotated `v0.9.5-rc.1` tag on that `main` commit and
   push it. Wait for the tag workflow and Go proxy publication before asking
   a trial application to pin `@v0.9.5-rc.1`. Fixes go to `main`; a second
   candidate gets a new tag such as `v0.9.5-rc.2`.
5. Once the candidate has no release blockers and the dated changelog is in
   the commit, rerun the gates on that final `main` commit. Create and push an
   annotated `v0.9.5` tag pointing at it. The tag workflow must finish before
   announcing the version. Verify the GitHub Release, its source archives,
   and the Go proxy module
   version, then install the released generator in a separate application and
   confirm the generated project builds and starts without a local `replace`.

Do not merge a candidate by changing `main`'s ref beneath a dirty checkout.
Do not call a green local unit test, a candidate branch, or a pushed tag alone
a finished release. If verification fails after a tag, stop promotion, fix on
`main`, and use a new tag; never force-push a replacement tag. Existing
applications must assess whether returning to their prior build reintroduces
the security defects fixed by this release; prefer a forward fix. Database
migrations and policy data require a separate, reviewed
rollback decision.

## 发布流程（简体中文）

`main` 是唯一发布主线。将审阅通过的发布改动通过 PR squash 为一个提交，
保留 `Unreleased`，将本版内容写入带日期的 `v0.9.5` changelog，同步更新
中英文 README、升级说明和安全策略。

最终提交必须通过完整质量检查、与 `v0.9.4` 的 API 对比，以及真实
PostgreSQL、MySQL、Redis 集成检查。扫描源码和待发布历史中的凭据，人工核验
扫描结果；本地约定、配置、密钥和构建产物不能进入发布。

main 检查通过后创建不可移动的附注标签，等待标签工作流再次验证并发布。
GitHub Release 包含本版双语 changelog 和比较链接；仅提供源码及 Go 模块，
不发布预编译安装包。最后从 Go 代理安装已发布的生成器，生成无本地 replace
的新项目并验证构建、迁移、CRUD 和正常退出。

`v0.9.4` 的历史发布流水线失败记录继续保留；新版本必须完成整个标签工作流
后才能宣布发布成功。Agent/MCP 模块仍为实验模块，根模块标签不发布它们。
