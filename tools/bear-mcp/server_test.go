package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubCLI installs a fake bear binary that records its argv and replays
// canned JSON. It proves argv construction without shell strings and without
// running the real CLI.
func stubCLI(t *testing.T, reply string) (stub string, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	jsonFile := filepath.Join(dir, "reply.json")
	if err := os.WriteFile(jsonFile, []byte(reply), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '<%s>' \"$@\" >> \"$STUB_ARGV\"\nprintf '\\n' >> \"$STUB_ARGV\"\ncat \"$STUB_JSON\"\n"
	stub = filepath.Join(dir, "bear")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_ARGV", argvFile)
	t.Setenv("STUB_JSON", jsonFile)
	return stub, argvFile
}

func readArgv(t *testing.T, argvFile string) string {
	t.Helper()
	contents, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("stub was not invoked: %v", err)
	}
	return string(contents)
}

func testServer(t *testing.T, stub, root string) *Server {
	t.Helper()
	server, err := NewServer(Config{CLIBin: stub, Roots: []string{root}})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func testProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/mcpcheck\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "application.yaml"), []byte("server:\n  port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestToolListIsStable(t *testing.T) {
	server, err := NewServer(Config{CLIBin: "/bin/true", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	got := server.ToolNames()
	want := []string{"project_info", "doctor", "gen_preview"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestBindDirRejectsTraversalAndOutside(t *testing.T) {
	root := testProject(t)
	outside := t.TempDir()
	stub, _ := stubCLI(t, "{}")
	server := testServer(t, stub, root)

	if _, err := server.bindDir(root); err != nil {
		t.Fatalf("root rejected: %v", err)
	}
	if _, err := server.bindDir(filepath.Join(root, "sub")); err == nil {
		// sub does not exist: EvalSymlinks fails -> rejected. Create it first.
		t.Fatalf("missing subdir accepted")
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := server.bindDir(filepath.Join(root, "sub")); err != nil {
		t.Fatalf("subdir rejected: %v", err)
	}
	for name, input := range map[string]string{
		"empty":    "",
		"relative": "some/relative",
		"dotdot":   filepath.Join(root, "..", "escape"),
		"outside":  outside,
		"overlong": string(make([]byte, maxPathLen+1)),
	} {
		if _, err := server.bindDir(input); err == nil {
			t.Fatalf("%s input %q accepted", name, input)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := server.bindDir(filepath.Join(root, "link")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestDoctorArgvIsFixed(t *testing.T) {
	root := testProject(t)
	stub, argvFile := stubCLI(t, `{"schema_version":1,"status":"pass","checks":[]}`)
	server := testServer(t, stub, root)

	if _, _, err := server.doctor(context.Background(), &mcp.CallToolRequest{}, DoctorInput{ProjectDir: root}); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if got := readArgv(t, argvFile); !strings.Contains(got, "<doctor><--format><json>") {
		t.Fatalf("doctor argv = %q", got)
	}
	if _, _, err := server.doctor(context.Background(), &mcp.CallToolRequest{}, DoctorInput{ProjectDir: root, Probe: true}); err != nil {
		t.Fatalf("probed doctor: %v", err)
	}
	if got := readArgv(t, argvFile); !strings.Contains(got, "<doctor><--format><json><--probe>") {
		t.Fatalf("probed doctor argv = %q", got)
	}
}

func TestGenPreviewAlwaysDryRun(t *testing.T) {
	root := testProject(t)
	stub, argvFile := stubCLI(t, `{"schema_version":1}`)
	server := testServer(t, stub, root)

	// Hostile-looking fields travel as one argv element, never a shell string.
	_, _, err := server.genPreview(context.Background(), &mcp.CallToolRequest{}, GenPreviewInput{
		ProjectDir: root,
		Kind:       "API",
		Name:       "invoice",
		Fields:     "name:string; rm -rf /,x:string",
	})
	if err != nil {
		t.Fatalf("genPreview: %v", err)
	}
	got := readArgv(t, argvFile)
	want := "<gen><api><invoice><--fields><name:string; rm -rf /,x:string><--dry-run><--format><json>"
	if !strings.Contains(got, want) {
		t.Fatalf("preview argv = %q, want %q", got, want)
	}

	if _, _, err := server.genPreview(context.Background(), &mcp.CallToolRequest{}, GenPreviewInput{
		ProjectDir: root, Kind: "service", Name: "invoice",
	}); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if _, _, err := server.genPreview(context.Background(), &mcp.CallToolRequest{}, GenPreviewInput{
		ProjectDir: root, Kind: "api", Name: "invoice", Fields: strings.Repeat("x", maxFieldsLen+1),
	}); err == nil {
		t.Fatal("oversized fields accepted")
	}
	if _, _, err := server.genPreview(context.Background(), &mcp.CallToolRequest{}, GenPreviewInput{
		ProjectDir: root, Kind: "api", Name: "invoice", Config: []string{"../escape.yaml"},
	}); err == nil {
		t.Fatal("traversal config accepted")
	}
	before, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	// Rejected inputs must never reach the CLI: argv file unchanged.
	if _, _, err := server.genPreview(context.Background(), &mcp.CallToolRequest{}, GenPreviewInput{
		ProjectDir: "/etc", Kind: "api", Name: "invoice",
	}); err == nil {
		t.Fatal("outside project accepted")
	}
	after, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected input still invoked the CLI")
	}
}

func TestProjectInfoReadsWithoutSubprocess(t *testing.T) {
	root := testProject(t)
	if err := os.MkdirAll(filepath.Join(root, ".bear"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"module":"example.com/mcpcheck","framework_version":"v0.0.0","template_version":1,"apis":[]}`
	if err := os.WriteFile(filepath.Join(root, ".bear", "scaffold.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	stub, _ := stubCLI(t, "{}")
	server := testServer(t, stub, root)
	_, output, err := server.projectInfo(context.Background(), &mcp.CallToolRequest{}, ProjectInfoInput{ProjectDir: root})
	if err != nil {
		t.Fatalf("projectInfo: %v", err)
	}
	if output.Module != "example.com/mcpcheck" || !output.Manifest || !output.HasBearGoMod {
		t.Fatalf("projectInfo = %+v", output)
	}
	if _, _, err := server.projectInfo(context.Background(), &mcp.CallToolRequest{}, ProjectInfoInput{ProjectDir: t.TempDir()}); err == nil {
		t.Fatal("project without go.mod accepted")
	}
}

func TestOutputCapIsEnforced(t *testing.T) {
	root := testProject(t)
	big := strings.Repeat("x", maxOutputBytes+1024)
	stub, _ := stubCLI(t, big)
	server := testServer(t, stub, root)
	if _, _, err := server.doctor(context.Background(), &mcp.CallToolRequest{}, DoctorInput{ProjectDir: root}); err == nil {
		t.Fatal("oversized CLI output accepted")
	}
}

func TestNewServerRequiresBinding(t *testing.T) {
	if _, err := NewServer(Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := NewServer(Config{CLIBin: "relative/bear", Roots: []string{t.TempDir()}}); err == nil {
		t.Fatal("relative CLI path accepted")
	}
	if _, err := NewServer(Config{CLIBin: "/bin/true", Roots: nil}); err == nil {
		t.Fatal("empty roots accepted")
	}
	if _, err := NewServer(Config{CLIBin: "/bin/true", Roots: []string{filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Fatal("missing root accepted")
	}
}
