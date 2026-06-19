package main

import (
	"strings"
	"testing"
)

func TestExportedName(t *testing.T) {
	tests := map[string]string{
		"user":         "User",
		"user_profile": "UserProfile",
		"user-profile": "UserProfile",
		"API_key":      "APIKey",
	}

	for input, want := range tests {
		if got := exportedName(input); got != want {
			t.Fatalf("exportedName(%q) = %q, want %q", input, got, want)
		}
	}
}

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

func TestGeneratedDockerignoreContentExcludesLocalAndSupplyChainFiles(t *testing.T) {
	got := generatedDockerignoreContent()

	for _, want := range []string{
		".git",
		".github",
		"docs/superpowers",
		"*_test.go",
		"sbom.spdx.json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated .dockerignore missing %q:\n%s", want, got)
		}
	}
}
