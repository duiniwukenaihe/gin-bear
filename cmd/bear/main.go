package main

import (
	"os"
	"strings"
	"unicode"

	"github.com/duiniwukenaihe/gin-bear/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}

func exportedName(input string) string {
	parts := strings.FieldsFunc(input, func(r rune) bool {
		return r == '-' || r == '_' || unicode.IsSpace(r)
	})
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(exportedPart(part))
	}
	return b.String()
}

func exportedPart(part string) string {
	if part == "" {
		return ""
	}
	if part == strings.ToUpper(part) {
		return part
	}
	runes := []rune(part)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
