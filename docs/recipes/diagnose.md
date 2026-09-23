# Recipe: diagnose a project

Goal: find out why a project does not start or generate, without running it.

```sh
cd <project>
bear doctor --format json                # static, read-only, offline-safe
bear doctor --probe --probe-timeout 3s   # plus bounded dependency dials
```

Read `status` first (`pass`, `warn`, `fail`), then the failing check's
`message` and `remediation`. Exit codes: 0 nothing blocking, 1 a check
failed, 2 bad flags. Stdout is exactly one JSON document; secrets, tokens,
and DSNs never appear in it.

Common findings:

| Check | Meaning | Fix |
| --- | --- | --- |
| `config` fail mentioning jwt | production needs a secret | set `BEAR_AUTH_JWT_SECRET` |
| `manifest` fail | `.bear/scaffold.json` broken | restore from version control |
| `framework` warn | CLI/project version drift | align versions before generating |
| `probe-database` fail | dependency down | start it, re-run `--probe` |

Non-goals: fixing code, connecting to production, printing secrets. The
contract is pinned by `TestDoctor*` in `internal/cli/doctor_test.go`.
