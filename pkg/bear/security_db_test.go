package bear

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type gormParamsFilter interface {
	ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
}

func TestBuildGormConfigAlwaysParameterizesQueries(t *testing.T) {
	for _, cfg := range []*DBConfig{{}, {SlowQueryThreshold: "1ms"}} {
		gormConfig := buildGormConfig(cfg)
		filter, ok := gormConfig.Logger.(gormParamsFilter)
		if !ok {
			t.Fatalf("GORM logger %T has no ParamsFilter", gormConfig.Logger)
		}
		sql, params := filter.ParamsFilter(
			context.Background(),
			"SELECT * FROM users WHERE password = ?",
			"slow-query-binding-secret",
		)
		if sql != "SELECT * FROM users WHERE password = ?" {
			t.Fatalf("filtered SQL = %q", sql)
		}
		if len(params) != 0 {
			t.Fatalf("parameterized logger retained binding params: %#v", params)
		}
	}
}

func TestValidateProductionDBTLSPostgresStructuredAndRawDSN(t *testing.T) {
	t.Setenv("PGSSLMODE", "")
	t.Setenv("PGSSLROOTCERT", "")
	tests := []struct {
		name    string
		config  *DBConfig
		wantErr bool
	}{
		{
			name: "structured verify full",
			config: &DBConfig{
				Enabled: true, Type: "postgres", Host: "db.example", DBName: "app",
				PostgresSSLMode: "verify-full",
			},
		},
		{
			name: "structured verify ca",
			config: &DBConfig{
				Enabled: true, Type: "postgres", Host: "db.example", DBName: "app",
				PostgresSSLMode: "verify-ca",
			},
			wantErr: true,
		},
		{
			name: "structured require",
			config: &DBConfig{
				Enabled: true, Type: "postgres", Host: "db.example", DBName: "app",
				PostgresSSLMode: "require",
			},
			wantErr: true,
		},
		{
			name: "raw URL verify full",
			config: &DBConfig{
				Enabled: true, Type: "postgres",
				DSN: "postgres://user:raw-secret@db.example/app?sslmode=verify-full",
			},
		},
		{
			name: "raw URL disable",
			config: &DBConfig{
				Enabled: true, Type: "postgres",
				DSN: "postgres://user:raw-secret@db.example/app?sslmode=disable",
			},
			wantErr: true,
		},
		{
			name: "raw keyword verify full",
			config: &DBConfig{
				Enabled: true, Type: "postgres",
				DSN: "host=db.example user=user password='raw secret' dbname=app sslmode=verify-full",
			},
		},
		{
			name: "raw keyword missing mode",
			config: &DBConfig{
				Enabled: true, Type: "postgres",
				DSN: "host=db.example user=user password='raw secret' dbname=app",
			},
			wantErr: true,
		},
		{
			name: "malformed raw DSN",
			config: &DBConfig{
				Enabled: true, Type: "postgres", DSN: "host='unterminated",
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateProductionDBTLS(test.config, true)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateProductionDBTLS() error = %v, wantErr=%v", err, test.wantErr)
			}
			if err != nil && containsAny(err.Error(), "raw-secret", "raw secret") {
				t.Fatalf("validation error leaked DSN credential: %v", err)
			}
		})
	}
}

func TestValidateProductionDBTLSMySQLStructuredAndRawDSN(t *testing.T) {
	const secureTLSName = "gin-bear-security-test-verify"
	const insecureTLSName = "gin-bear-security-test-insecure"
	if err := mysqldriver.RegisterTLSConfig(secureTLSName, &tls.Config{MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mysqldriver.DeregisterTLSConfig(secureTLSName) })
	if err := mysqldriver.RegisterTLSConfig(insecureTLSName, &tls.Config{InsecureSkipVerify: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mysqldriver.DeregisterTLSConfig(insecureTLSName) })

	tests := []struct {
		name    string
		config  *DBConfig
		wantErr bool
	}{
		{name: "structured true", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app", TLS: "true"}},
		{name: "structured secure custom", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app", TLS: secureTLSName}},
		{name: "structured missing", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app"}, wantErr: true},
		{name: "structured false", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app", TLS: "false"}, wantErr: true},
		{name: "structured skip verify", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app", TLS: "skip-verify"}, wantErr: true},
		{name: "structured preferred", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app", TLS: "preferred"}, wantErr: true},
		{name: "structured insecure custom", config: &DBConfig{Enabled: true, Type: "mysql", DBName: "app", TLS: insecureTLSName}, wantErr: true},
		{
			name:   "raw true",
			config: &DBConfig{Enabled: true, Type: "mysql", DSN: "user:raw-secret@tcp(db.example:3306)/app?tls=true"},
		},
		{
			name:    "raw preferred",
			config:  &DBConfig{Enabled: true, Type: "mysql", DSN: "user:raw-secret@tcp(db.example:3306)/app?tls=preferred"},
			wantErr: true,
		},
		{
			name:    "malformed raw DSN",
			config:  &DBConfig{Enabled: true, Type: "mysql", DSN: "not a mysql DSN?password=raw-secret"},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateProductionDBTLS(test.config, true)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateProductionDBTLS() error = %v, wantErr=%v", err, test.wantErr)
			}
			if err != nil && containsAny(err.Error(), "raw-secret") {
				t.Fatalf("validation error leaked DSN credential: %v", err)
			}
		})
	}
}

func TestValidateProductionDBTLSSkipsNonProductionAndDisabledDB(t *testing.T) {
	insecure := &DBConfig{Enabled: true, Type: "postgres", PostgresSSLMode: "disable"}
	if err := validateProductionDBTLS(insecure, false); err != nil {
		t.Fatalf("non-production config rejected: %v", err)
	}
	insecure.Enabled = false
	if err := validateProductionDBTLS(insecure, true); err != nil {
		t.Fatalf("disabled database config rejected: %v", err)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
