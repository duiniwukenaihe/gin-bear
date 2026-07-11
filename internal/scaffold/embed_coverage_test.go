package scaffold

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRenderTemplateReportsParseAndMissingValueErrors(t *testing.T) {
	if _, err := renderTemplate("broken", []byte("{{"), Options{}); err == nil || !strings.Contains(err.Error(), "parse template") {
		t.Fatalf("parse error = %v", err)
	}
	if _, err := renderTemplate("missing", []byte("{{.Unknown}}"), Options{}); err == nil || !strings.Contains(err.Error(), "render template") {
		t.Fatalf("missing-value error = %v", err)
	}
	contents, err := renderTemplate("yaml", []byte(`{{yamlScalar .Name}}`), Options{Name: `billing "critical": api`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `billing "critical": api`) {
		t.Fatalf("YAML scalar output = %q", contents)
	}
}

func TestRenderTemplateTreeRejectsInvalidSourcesAndCancellation(t *testing.T) {
	destination := t.TempDir()
	missingRoot := fstest.MapFS{"other/file.tmpl": {Data: []byte("value")}}
	if err := renderTemplateTree(context.Background(), missingRoot, "template", destination, Options{}); err == nil {
		t.Fatal("missing template root was accepted")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	valid := fstest.MapFS{"template/file.tmpl": {Data: []byte("value")}}
	if err := renderTemplateTree(cancelled, valid, "template", destination, Options{}); err == nil || !errorsIsContextCanceled(err) {
		t.Fatalf("cancelled render error = %v", err)
	}

	invalidGo := fstest.MapFS{"template/broken.go.tmpl": {Data: []byte("package")}}
	if err := renderTemplateTree(context.Background(), invalidGo, "template", destination, Options{}); err == nil || !strings.Contains(err.Error(), "format generated Go file") {
		t.Fatalf("invalid Go template error = %v", err)
	}
}

func errorsIsContextCanceled(err error) bool {
	return err == context.Canceled
}
