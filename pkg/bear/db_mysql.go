//go:build !bear_no_mysql

package bear

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const mysqlDriverAvailable = true

func mysqlDSN(cfg *DBConfig, host, port, user, dbname string) (string, error) {
	if port == "" {
		port = "3306"
	}
	driverConfig := mysqldriver.NewConfig()
	driverConfig.User = user
	driverConfig.Passwd = cfg.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(host, port)
	driverConfig.DBName = dbname
	driverConfig.Params = map[string]string{"charset": "utf8mb4"}
	driverConfig.ParseTime = true
	driverConfig.Loc = time.Local
	driverConfig.TLSConfig = strings.TrimSpace(cfg.TLS)
	driverConfig.ClientFoundRows = true
	dsn := driverConfig.FormatDSN()
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL DSN configuration: %w", err)
	}
	if parsed.User != driverConfig.User || parsed.Passwd != driverConfig.Passwd || parsed.DBName != driverConfig.DBName {
		return "", errors.New("MySQL user, password, or database name contains characters that cannot be represented safely in a DSN")
	}
	return dsn, nil
}

func mysqlDialector(dsn string) (gorm.Dialector, error) {
	return mysql.Open(dsn), nil
}

func validateProductionMySQLTLS(dsn string) error {
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return errors.New("production MySQL DSN is invalid")
	}
	if parsed.TLS == nil || parsed.TLS.InsecureSkipVerify || parsed.AllowFallbackToPlaintext {
		return errors.New("production MySQL requires TLS with certificate verification")
	}
	return nil
}

func databaseStartupDSN(ctx context.Context, dbType, dsn string) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(dbType), "mysql") {
		return dsn, nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return dsn, nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return "", fmt.Errorf("database startup canceled: %w", context.DeadlineExceeded)
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", errors.New("invalid MySQL DSN configuration")
	}
	config.Timeout = boundedDatabaseTimeout(config.Timeout, remaining)
	config.ReadTimeout = boundedDatabaseTimeout(config.ReadTimeout, remaining)
	config.WriteTimeout = boundedDatabaseTimeout(config.WriteTimeout, remaining)
	return config.FormatDSN(), nil
}

func boundedDatabaseTimeout(configured, remaining time.Duration) time.Duration {
	if configured <= 0 || configured > remaining {
		return remaining
	}
	return configured
}
