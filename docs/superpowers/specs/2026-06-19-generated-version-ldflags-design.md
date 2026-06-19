# Generated Version Ldflags Design

## Problem

The runtime `/version` endpoint depends on Go linker flags that target the package containing `bear.Version`, `bear.Commit`, and `bear.BuildTime`. After `bear-cli new` clones the full template and rewrites Go imports to a new module name, generated build artifacts still pointed at `github.com/duiniwukenaihe/gin-bear/pkg/bear`. That can leave generated applications reporting default build metadata even when Docker build args or CI build flags are provided.

There are two generator modes with different package ownership:

- `bear-cli new` clones the full repository and rewrites imports to the generated module. Its Dockerfile must target `<module>/pkg/bear`.
- The legacy `bear new` command creates a lightweight application that imports the upstream framework package. Its Dockerfile must keep `github.com/duiniwukenaihe/gin-bear/pkg/bear`.

## Design

Add a small build metadata rewrite helper to `cmd/bear-cli/cmd/new.go` and call it after Go import rewriting for generated build artifacts. The helper rewrites only the linker metadata package path, keeping the rest of the Dockerfile and workflow untouched.

Keep the legacy `cmd/bear` Dockerfile behavior explicit by moving the Dockerfile string into a focused helper and testing that it still targets the upstream framework package. This prevents future maintenance from applying the full-template rewrite to the lightweight generator by mistake.

## Testing

Add unit tests for both generator modes:

- `cmd/bear-cli/cmd` verifies the cloned-template Dockerfile rewrites all three linker variables to `my-app/pkg/bear`.
- `cmd/bear-cli/cmd` verifies the rewrite path list includes both `Dockerfile` and `.github/workflows/ci.yml`.
- `cmd/bear` verifies the legacy generated Dockerfile keeps all three linker variables pointed at `github.com/duiniwukenaihe/gin-bear/pkg/bear` and does not target a nonexistent local framework package.
