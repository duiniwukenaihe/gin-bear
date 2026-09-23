//go:build bear_no_mysql

package bear

import (
	"strings"
	"testing"
)

func TestMySQLDisabledBuildTagRejectsSelection(t *testing.T) {
	_, err := buildDSN(&DBConfig{Type: "mysql", Host: "localhost", DBName: "app"})
	if err == nil || !strings.Contains(err.Error(), "bear_no_mysql") {
		t.Fatalf("MySQL selection error = %v, want excluded-driver error", err)
	}
}
