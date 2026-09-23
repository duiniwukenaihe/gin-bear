//go:build bear_no_sqlite

package bear

import (
	"errors"

	"gorm.io/gorm"
)

const sqliteDriverAvailable = false

func sqliteDialector(string) (gorm.Dialector, error) {
	return nil, errors.New("SQLite support is excluded by the bear_no_sqlite build tag")
}
