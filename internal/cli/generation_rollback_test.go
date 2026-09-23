package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecutePlanRollsBackPartiallyWrittenMigration is the B4 acceptance: when
// the down migration fails after the up file was already written, the up file
// must be removed together with the resource package instead of being left
// behind as a half migration pair.
func TestExecutePlanRollsBackPartiallyWrittenMigration(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, migrationDirectory), 0755); err != nil {
		t.Fatal(err)
	}
	downRel := filepath.Join(migrationDirectory, "001_create_invoice.down.sql")
	if err := os.WriteFile(filepath.Join(project, downRel), []byte("DROP TABLE invoice;\n"), 0644); err != nil {
		t.Fatal(err)
	}

	plan := &resourcePlan{
		Kind:   "api",
		Target: filepath.Join("internal", "invoice", "module.go"),
		Files: []plannedFile{{
			Name:     "module.go",
			Rel:      "internal/invoice/module.go",
			Contents: []byte("package invoice\n"),
		}},
		Migration: &plannedMigration{Version: "001", Name: "create_invoice"},
		migration: resourceMigration{
			Version: "001",
			Name:    "create_invoice",
			UpSQL:   "CREATE TABLE invoice (id integer);\n",
			DownSQL: "DROP TABLE invoice;\n",
		},
		data: resourceData{PackageName: "invoice"},
	}

	_, err := executePlan(context.Background(), project, plan, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("executePlan error = %v, want the pre-existing migration conflict", err)
	}
	if _, statErr := os.Stat(filepath.Join(project, "internal", "invoice")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed generation kept the resource package: %v", statErr)
	}
	upRel := filepath.Join(migrationDirectory, "001_create_invoice.up.sql")
	if _, statErr := os.Stat(filepath.Join(project, upRel)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed generation left a half migration pair (up.sql present): %v", statErr)
	}
}
