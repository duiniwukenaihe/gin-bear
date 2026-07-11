package cmd

import (
	"os"
	"strings"
)

var newCmd = legacyCommand("new")

func updateFile(path, old, new string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	newContent := strings.ReplaceAll(string(content), old, new)
	return os.WriteFile(path, []byte(newContent), 0644)
}

func rewriteFile(path string, rewrite func(string) string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rewrite(string(content))), 0644)
}

func rewriteGoModModule(content, moduleName string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "module ") {
			lines[i] = "module " + moduleName
			return strings.Join(lines, "\n")
		}
	}
	return "module " + moduleName + "\n\n" + content
}

func rewriteGoImports(content, moduleName string) string {
	content = strings.ReplaceAll(content, "\"bear/", "\""+moduleName+"/")
	content = strings.ReplaceAll(content, "\"github.com/duiniwukenaihe/gin-bear/", "\""+moduleName+"/")
	return content
}
