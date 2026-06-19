package cmd

import (
	"strings"
	"testing"
)

func TestRewriteGoModModuleLine(t *testing.T) {
	input := "module github.com/duiniwukenaihe/gin-bear\n\ngo 1.25.0\n"

	got := rewriteGoModModule(input, "my-app")

	want := "module my-app\n\ngo 1.25.0\n"
	if got != want {
		t.Fatalf("go.mod rewrite = %q", got)
	}
}

func TestRewriteGoImports(t *testing.T) {
	input := `package main

import (
	"bear/pkg/bear"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear/gen"
)
`

	got := rewriteGoImports(input, "my-app")

	if got == input {
		t.Fatal("expected imports to change")
	}
	if want := `"my-app/pkg/bear"`; !strings.Contains(got, want) {
		t.Fatalf("missing %s in %s", want, got)
	}
	if want := `"my-app/pkg/bear/gen"`; !strings.Contains(got, want) {
		t.Fatalf("missing %s in %s", want, got)
	}
}

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

func TestBuildMetadataRewritePathsIncludesDockerfileAndCI(t *testing.T) {
	got := buildMetadataRewritePaths("my-app")

	want := []string{
		"my-app/Dockerfile",
		"my-app/.github/workflows/ci.yml",
	}
	if len(got) != len(want) {
		t.Fatalf("path count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
