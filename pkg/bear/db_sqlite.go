//go:build !bear_no_sqlite

package bear

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const sqliteDriverAvailable = true

func sqliteDialector(dsn string) (gorm.Dialector, error) {
	return sqlite.Open(dsn), nil
}
