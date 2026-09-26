# Security Policy

## Supported Versions

`v0.9.5` is the current supported release. Users upgrading from `v0.9.4`
should follow the [v0.9.5 upgrade guide](docs/upgrade-v0.9.4-to-v0.9.5.md).
Users upgrading from `v0.9.1` should also follow the
[v0.9.1 to v0.9.2 migration guide](docs/migration-v0.9.1-to-v0.9.2.md).

Generated applications should update from the scaffold regularly and run
`GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.26.6 make verify` before release.
Published tags are immutable Go module versions. GitHub provides source
archives and release metadata so a deployment can be traced to its source
commit.

## Reporting a Vulnerability

Please report suspected vulnerabilities privately to the repository maintainers instead of opening a public issue. Include affected version or commit, reproduction steps, impact, and any known workaround.

## Security Updates

Security fixes should include tests when practical, a clear changelog or commit message, and a fresh `govulncheck ./...` result.

## 安全策略（简体中文）

当前支持版本为 `v0.9.5`。从 `v0.9.4` 升级请阅读
[中英文升级说明](docs/upgrade-v0.9.4-to-v0.9.5.md#简体中文)。
生成项目也应及时更新依赖并完成应用验收。生产密钥、令牌、连接串及本地配置
不得提交到仓库或发布说明。

发现疑似漏洞时，请私下联系仓库维护者，提供受影响版本、复现步骤、影响和
已知规避方式，避免先创建公开 issue。安全修复应附回归测试、明确的变更说明
以及新的漏洞扫描结果。已发布标签保持不变，修复通过新版本交付。
