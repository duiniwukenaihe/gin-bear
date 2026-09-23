//go:build !bear_no_casbin

package bear

import (
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresCasbinAdapterRequiresManagedTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	adapter, err := NewPostgresCasbinAdapter(db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule").
		WillReturnError(errors.New("relation casbin_rule does not exist"))
	if _, err := NewCasbinAuthorizerWithAdapter(adapter, nil); err == nil || !strings.Contains(err.Error(), "casbin_rule") {
		t.Fatalf("missing managed table error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCasbinAdapterDeletesExactRule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	adapter, err := NewPostgresCasbinAdapter(db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("DELETE FROM casbin_rule WHERE ptype = \\$1 AND COALESCE\\(v0, ''\\) = \\$2").
		WithArgs("p", "admin", "/x", "GET", "", "", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := adapter.RemovePolicy("p", "p", []string{"admin", "/x", "GET"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCasbinAdapterSavePolicyRollsBackOnWriteFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	adapter, err := NewPostgresCasbinAdapter(db)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildCasbinModel(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy["p"]["p"].Policy = [][]string{{"admin", "/secret", "GET"}}
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM casbin_rule").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO casbin_rule").WillReturnError(errors.New("write unavailable"))
	mock.ExpectRollback()
	if err := adapter.SavePolicy(policy); err == nil || !strings.Contains(err.Error(), "write unavailable") {
		t.Fatalf("SavePolicy error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCasbinAdapterFilteredRemovalUsesOnlySelectedFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	adapter, err := NewPostgresCasbinAdapter(db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("DELETE FROM casbin_rule WHERE ptype = \\$1 AND v0 = \\$2 AND v2 = \\$3").
		WithArgs("p", "admin", "GET").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := adapter.RemoveFilteredPolicy("p", "p", 0, "admin", "", "GET"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
