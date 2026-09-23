package bear

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type requestContextModel struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func newRequestContextRepository(t *testing.T) *Repository[requestContextModel] {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB failed: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&requestContextModel{}); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}
	return &Repository[requestContextModel]{Adapter: &GormAdapter{DB: db}}
}

func newGinContextWithRequest(cancelled context.Context) *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/items", nil).WithContext(cancelled)
	ctx.Request = req
	return ctx
}

// TestRepositoryRequestContextCanceledRequest reproduces production issue B:
// an already-canceled HTTP request passed as *gin.Context still succeeds.
func TestRepositoryRequestContextCanceledRequest(t *testing.T) {
	repo := newRequestContextRepository(t)
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()
	ginCtx := newGinContextWithRequest(reqCtx)

	var value int
	err := repo.DB(ginCtx).Raw("SELECT 1").Scan(&value).Error
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request query err = %v, want context.Canceled", err)
	}
}

func TestRepositoryRequestContextDeadlineExceeded(t *testing.T) {
	repo := newRequestContextRepository(t)
	reqCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	ginCtx := newGinContextWithRequest(reqCtx)

	var value int
	err := repo.DB(ginCtx).Raw("SELECT 1").Scan(&value).Error
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired request query err = %v, want context.DeadlineExceeded", err)
	}
}

func TestRepositoryRequestContextNativeCancellation(t *testing.T) {
	repo := newRequestContextRepository(t)
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var value int
	err := repo.DB(reqCtx).Raw("SELECT 1").Scan(&value).Error
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("native canceled query err = %v, want context.Canceled", err)
	}
}

type requestValueKey struct{}

func TestRepositoryRequestContextPropagatesValues(t *testing.T) {
	repo := newRequestContextRepository(t)
	reqCtx := context.WithValue(context.Background(), requestValueKey{}, "trace-123")
	reqCtx = WithUserID(reqCtx, "user-7")
	ginCtx := newGinContextWithRequest(reqCtx)

	stmtCtx := repo.DB(ginCtx).Statement.Context
	if stmtCtx == nil {
		t.Fatal("Statement.Context is nil")
	}
	if got := stmtCtx.Value(requestValueKey{}); got != "trace-123" {
		t.Fatalf("request value = %v, want trace-123", got)
	}
	if got, _ := UserIDFromContext(stmtCtx); got != "user-7" {
		t.Fatalf("user id = %q, want user-7", got)
	}
}

func TestRepositoryRequestContextTransactionRollback(t *testing.T) {
	repo := newRequestContextRepository(t)
	ginCtx := newGinContextWithRequest(context.Background())
	tx := repo.Adapter.DB.Begin()
	if tx.Error != nil {
		t.Fatalf("Begin failed: %v", tx.Error)
	}
	ginCtx.Set(txKey, tx)

	if err := repo.DB(ginCtx).Create(&requestContextModel{Name: "tx-row"}).Error; err != nil {
		_ = tx.Rollback()
		t.Fatalf("tx create failed: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	var count int64
	if err := repo.Adapter.DB.Model(&requestContextModel{}).Where("name = ?", "tx-row").Count(&count).Error; err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("count after rollback = %d, want 0 (write escaped transaction)", count)
	}
}

func TestRepositoryRequestContextCanceledInsideTransaction(t *testing.T) {
	repo := newRequestContextRepository(t)
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()
	ginCtx := newGinContextWithRequest(reqCtx)
	tx := repo.Adapter.DB.WithContext(context.Background()).Begin()
	if tx.Error != nil {
		t.Fatalf("Begin failed: %v", tx.Error)
	}
	defer func() { _ = tx.Rollback() }()
	ginCtx.Set(txKey, tx)

	var value int
	err := repo.DB(ginCtx).Raw("SELECT 1").Scan(&value).Error
	if !errors.Is(err, context.Canceled) {
		_ = tx.Rollback()
		t.Fatalf("canceled tx query err = %v, want context.Canceled (must not fall back to plain connection)", err)
	}
	_ = tx.Rollback()
}

func TestRepositoryRequestContextCompatibility(t *testing.T) {
	repo := newRequestContextRepository(t)
	// No context at all keeps the previous behaviour and must not panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DB() without context panicked: %v", r)
			}
		}()
		if repo.DB().Statement == nil {
			t.Fatal("DB() without context returned nil statement")
		}
	}()
	// Valid Gin without Request must not panic either.
	gin.SetMode(gin.TestMode)
	bare, _ := gin.CreateTestContext(httptest.NewRecorder())
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DB() with Gin lacking Request panicked: %v", r)
			}
		}()
		var value int
		if err := repo.DB(bare).Raw("SELECT 1").Scan(&value).Error; err != nil {
			t.Fatalf("DB() with Gin lacking Request err = %v", err)
		}
	}()
}
