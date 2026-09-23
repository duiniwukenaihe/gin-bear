//go:build bear_no_sqlite

package bear

import (
	"strings"
	"testing"
)

func TestSQLiteDisabledBuildTagRejectsSelection(t *testing.T) {
	dialector, err := sqliteDialector(":memory:")
	if dialector != nil || err == nil || !strings.Contains(err.Error(), "bear_no_sqlite") {
		t.Fatalf("SQLite selection = %v, %v; want excluded-driver error", dialector, err)
	}
}
