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
