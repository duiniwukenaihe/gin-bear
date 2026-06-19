# Generated Version Ldflags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make generated project build artifacts inject `/version` build metadata into the correct Go package for each scaffold mode.

**Architecture:** `bear-cli new` clones a full template and rewrites module imports, so it also rewrites Dockerfile and GitHub Actions linker package paths to the generated module. The legacy `bear new` command keeps using the upstream framework package because it generates only a lightweight application shell.

**Tech Stack:** Go, Cobra CLI, Dockerfile linker flags, Go unit tests.

---

### Task 1: Full-Template Build Metadata Rewrite

**Files:**
- Modify: `cmd/bear-cli/cmd/new.go`
- Test: `cmd/bear-cli/cmd/new_test.go`

- [x] **Step 1: Write the failing test**

```go
func TestRewriteBuildMetadataPackage(t *testing.T) {
	input := `RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/duiniwukenaihe/gin-bear/pkg/bear.Version=${VERSION} -X github.com/duiniwukenaihe/gin-bear/pkg/bear.Commit=${COMMIT} -X github.com/duiniwukenaihe/gin-bear/pkg/bear.BuildTime=${BUILD_TIME}" -o /out/app ./cmd`

	got := rewriteBuildMetadataPackage(input, "my-app")

	if strings.Contains(got, "github.com/duiniwukenaihe/gin-bear/pkg/bear") {
		t.Fatalf("upstream package path should be rewritten: %s", got)
	}
	for _, want := range []string{
		"-X my-app/pkg/bear.Version=${VERSION}",
		"-X my-app/pkg/bear.Commit=${COMMIT}",
		"-X my-app/pkg/bear.BuildTime=${BUILD_TIME}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear-cli/cmd -run TestRewriteBuildMetadataPackage -count=1`

Expected: FAIL with `undefined: rewriteBuildMetadataPackage`.

- [x] **Step 3: Implement the rewrite**

```go
for _, path := range buildMetadataRewritePaths(projectName) {
	if err := rewriteFileIfExists(path, func(content string) string {
		return rewriteBuildMetadataPackage(content, projectName)
	}); err != nil {
		fmt.Printf("Failed to update build metadata in %s: %v\n", path, err)
	}
}

func rewriteBuildMetadataPackage(content, moduleName string) string {
	return strings.ReplaceAll(content, "github.com/duiniwukenaihe/gin-bear/pkg/bear", moduleName+"/pkg/bear")
}

func buildMetadataRewritePaths(projectName string) []string {
	return []string{
		filepath.Join(projectName, "Dockerfile"),
		filepath.Join(projectName, ".github", "workflows", "ci.yml"),
	}
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear-cli/cmd -run TestRewriteBuildMetadataPackage -count=1`

Expected: PASS.

### Task 2: Legacy Generator Guard

**Files:**
- Modify: `cmd/bear/main.go`
- Test: `cmd/bear/main_test.go`

- [x] **Step 1: Write the failing guard test**

```go
func TestGeneratedDockerfileUsesFrameworkVersionPackage(t *testing.T) {
	got := generatedDockerfileContent()

	if strings.Contains(got, "demo-api/pkg/bear") {
		t.Fatalf("generated Dockerfile should not target a local framework package: %s", got)
	}
	for _, want := range []string{
		"-X github.com/duiniwukenaihe/gin-bear/pkg/bear.Version=${VERSION}",
		"-X github.com/duiniwukenaihe/gin-bear/pkg/bear.Commit=${COMMIT}",
		"-X github.com/duiniwukenaihe/gin-bear/pkg/bear.BuildTime=${BUILD_TIME}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in generated Dockerfile: %s", want, got)
		}
	}
}
```

- [x] **Step 2: Run test to verify it catches the wrong behavior**

Run: `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear -run TestGeneratedDockerfileUsesFrameworkVersionPackage -count=1`

Expected: FAIL if the legacy Dockerfile targets `demo-api/pkg/bear`.

- [x] **Step 3: Keep the legacy Dockerfile pointed at the upstream framework package**

```go
func generatedDockerfileContent() string {
	versionPackage := "github.com/duiniwukenaihe/gin-bear/pkg/bear"
	return fmt.Sprintf(`... -X %[1]s.Version=${VERSION} ...`, versionPackage)
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `GOPROXY=https://goproxy.cn,direct go test ./cmd/bear -run TestGeneratedDockerfileUsesFrameworkVersionPackage -count=1`

Expected: PASS.

### Task 3: Documentation And Verification

**Files:**
- Modify: `docs/production.md`
- Create: `docs/superpowers/specs/2026-06-19-generated-version-ldflags-design.md`
- Create: `docs/superpowers/plans/2026-06-19-generated-version-ldflags.md`

- [x] **Step 1: Document package-path behavior**

Add a Version Metadata note explaining that `bear-cli new` rewrites linker paths to `<module>/pkg/bear`, while legacy `bear new` keeps the upstream framework package.

- [x] **Step 2: Run production verification**

Run:

```bash
GOPROXY=https://goproxy.cn,direct go build ./cmd ./cmd/bear ./cmd/bear-cli
GOPROXY=https://goproxy.cn,direct go test ./... -count=1
GOPROXY=https://goproxy.cn,direct go test -race ./... -count=1
GOPROXY=https://goproxy.cn,direct go vet ./...
GOPROXY=https://goproxy.cn,direct $(go env GOPATH)/bin/govulncheck ./...
GOPROXY=https://goproxy.cn,direct go mod tidy
git diff --exit-code -- go.mod go.sum
```

Expected: all commands exit 0.
