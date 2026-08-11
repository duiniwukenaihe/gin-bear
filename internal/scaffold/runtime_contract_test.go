package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedProjectUsesProductionStartupAPI(t *testing.T) {
	project := filepath.Join(t.TempDir(), "production-api")
	if err := Generate(context.Background(), Options{
		Name:             "production-api",
		Module:           "example.com/production-api",
		Directory:        project,
		FrameworkVersion: "v0.9.2",
	}); err != nil {
		t.Fatal(err)
	}

	appSource, err := os.ReadFile(filepath.Join(project, "internal", "app", "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"application, err := bear.IgniteE()",
		`return fmt.Errorf("initialize application: %w", err)`,
		"application.Serve(ctx)",
	} {
		if !strings.Contains(string(appSource), want) {
			t.Errorf("generated app.go missing %q:\n%s", want, appSource)
		}
	}
	for _, forbidden := range []string{
		"bear.Ignite()",
		"application.ApplyAll(ctx)",
		"application.Launch(ctx)",
		"signal.NotifyContext",
	} {
		if strings.Contains(string(appSource), forbidden) {
			t.Errorf("generated app.go contains deprecated startup call %q:\n%s", forbidden, appSource)
		}
	}

	mainSource, err := os.ReadFile(filepath.Join(project, "cmd", "server", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(mainSource), "signal.NotifyContext"); count != 1 {
		t.Errorf("generated main.go signal context count = %d, want 1:\n%s", count, mainSource)
	}
}
