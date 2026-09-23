//go:build bear_no_mysql

package bear

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

const mysqlDriverAvailable = false

var errMySQLExcluded = errors.New("MySQL support is excluded by the bear_no_mysql build tag")

func mysqlDSN(*DBConfig, string, string, string, string) (string, error) {
	return "", errMySQLExcluded
}

func mysqlDialector(string) (gorm.Dialector, error) {
	return nil, errMySQLExcluded
}

func validateProductionMySQLTLS(string) error {
	return errMySQLExcluded
}

func databaseStartupDSN(_ context.Context, dbType, dsn string) (string, error) {
	if dbType == "mysql" {
		return "", errMySQLExcluded
	}
	return dsn, nil
}
