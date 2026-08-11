package bear

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type remainingAuditRecord struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	UpdatedBy string
}

func (*remainingAuditRecord) SetCreatedBy(string) {}
func (record *remainingAuditRecord) SetUpdatedBy(userID string) {
	record.UpdatedBy = userID
}

type remainingVersionedRecord struct {
	ID      uint `gorm:"primaryKey"`
	Name    string
	Version int64
}

func (record *remainingVersionedRecord) GetVersion() int64      { return record.Version }
func (record *remainingVersionedRecord) SetVersion(value int64) { record.Version = value }

func TestRepositoryUpdateByIDAppliesAuditUserToMapUpdates(t *testing.T) {
	adapter := newRemainingRepositoryAdapter(t, &remainingAuditRecord{})
	repository := NewRepository[remainingAuditRecord](adapter)
	record := remainingAuditRecord{Name: "before", UpdatedBy: "creator"}
	if err := repository.Create(context.Background(), &record); err != nil {
		t.Fatal(err)
	}

	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Set("current_user_id", "operator-7")
	updates := map[string]interface{}{"name": "after"}
	if err := repository.UpdateByID(ginCtx, record.ID, updates); err != nil {
		t.Fatal(err)
	}
	if _, mutated := updates["updated_by"]; mutated {
		t.Fatalf("UpdateByID mutated caller updates: %#v", updates)
	}
	loaded, err := repository.FindByID(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "after" || loaded.UpdatedBy != "operator-7" {
		t.Fatalf("updated record = %#v", loaded)
	}
}

func TestRepositoryOptimisticLockFailureRestoresCallerVersion(t *testing.T) {
	adapter := newRemainingRepositoryAdapter(t, &remainingVersionedRecord{})
	repository := NewRepository[remainingVersionedRecord](adapter)
	stored := remainingVersionedRecord{Name: "stored", Version: 2}
	if err := repository.Create(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}

	stale := remainingVersionedRecord{ID: stored.ID, Name: "stale", Version: 1}
	if err := repository.Update(context.Background(), &stale); !errors.Is(err, ErrOptimisticLock) {
		t.Fatalf("Update() error = %v, want ErrOptimisticLock", err)
	}
	if stale.Version != 1 {
		t.Fatalf("caller version = %d, want restored version 1", stale.Version)
	}
}

func TestRepositoryZeroRowsReturnsNotFoundWithoutFollowUpRace(t *testing.T) {
	adapter := newRemainingRepositoryAdapter(t, &remainingAuditRecord{})
	repository := NewRepository[remainingAuditRecord](adapter)
	record := remainingAuditRecord{Name: "same"}
	if err := repository.Create(context.Background(), &record); err != nil {
		t.Fatal(err)
	}
	callbackName := "test:force_zero_rows"
	if err := adapter.Callback().Update().After("gorm:update").Register(callbackName, func(db *gorm.DB) {
		db.RowsAffected = 0
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Callback().Update().Remove(callbackName) })

	if err := repository.Update(context.Background(), &record); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("zero-row Update() error = %v, want gorm.ErrRecordNotFound", err)
	}
	missing := remainingAuditRecord{ID: record.ID + 1000, Name: "missing"}
	if err := repository.Update(context.Background(), &missing); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing Update() error = %v, want gorm.ErrRecordNotFound", err)
	}
	if err := repository.UpdateByID(context.Background(), missing.ID, map[string]interface{}{"name": "missing"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing UpdateByID() error = %v, want gorm.ErrRecordNotFound", err)
	}
}

func TestBuildMySQLDSNRequestsMatchedRowCounts(t *testing.T) {
	dsn, err := buildDSN(&DBConfig{
		Enabled: true,
		Type:    "mysql",
		Host:    "127.0.0.1",
		Port:    "3306",
		User:    "service",
		DBName:  "service",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.ClientFoundRows {
		t.Fatalf("MySQL DSN does not request matched row counts: %s", dsn)
	}
}

func newRemainingRepositoryAdapter(t *testing.T, models ...interface{}) *GormAdapter {
	t.Helper()
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")
	dsn := filepath.Join(t.TempDir(), fmt.Sprintf("repository-%s.db", t.Name()))
	adapter, err := NewGormAdapter(&DBConfig{Enabled: true, Type: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Shutdown() })
	return adapter
}
