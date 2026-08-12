package bear

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

const txKey = "bear_db_tx"

var ErrOptimisticLock = errors.New("optimistic lock failed: version mismatch")

// AuditModel 审计接口，自动记录操作人
type AuditModel interface {
	SetCreatedBy(userID string)
	SetUpdatedBy(userID string)
}

// VersionedModel 乐观锁接口
type VersionedModel interface {
	GetVersion() int64
	SetVersion(v int64)
}

// GormAdapter 是 GORM v2 的适配器
type GormAdapter struct {
	*gorm.DB
	shutdownOnce sync.Once
	shutdownErr  error
}

func (r *GormAdapter) Name() string {
	return "GormAdapter"
}

// buildDSN 构建数据库 DSN，支持 MySQL/PostgreSQL 或直接 DSN
func buildDSN(cfg *DBConfig) (string, error) {
	if cfg == nil {
		return "", errors.New("database config is required")
	}
	// 如果已配置 DSN，直接使用
	if cfg.DSN != "" {
		return cfg.DSN, nil
	}

	// 传统方式：拼接 DSN
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	user := cfg.User
	if user == "" {
		user = "root"
	}
	dbname := cfg.DBName

	dbType := cfg.Type
	if dbType == "" {
		dbType = "mysql" // 默认 MySQL
	}

	switch dbType {
	case "postgres", "postgresql":
		if port == "" {
			port = "5432"
		}
		sslmode, err := effectivePostgresSSLMode(cfg)
		if err != nil {
			return "", err
		}
		postgresURL := &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(user, cfg.Password),
			Host:   net.JoinHostPort(host, port),
			Path:   "/" + dbname,
		}
		postgresURL.RawPath = "/" + url.PathEscape(dbname)
		query := postgresURL.Query()
		query.Set("sslmode", sslmode)
		postgresURL.RawQuery = query.Encode()
		return postgresURL.String(), nil
	case "mysql":
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
	default:
		return "", fmt.Errorf("unsupported database type: %s, supported: mysql, postgres, sqlite", dbType)
	}
}

func effectivePostgresSSLMode(cfg *DBConfig) (string, error) {
	sslmode := strings.ToLower(strings.TrimSpace(cfg.PostgresSSLMode))
	if sslmode == "" {
		sslmode = strings.ToLower(strings.TrimSpace(cfg.SSLMode))
	}
	if sslmode == "" {
		sslmode = "disable"
	}
	switch sslmode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return sslmode, nil
	default:
		return "", fmt.Errorf("database PostgreSQL sslmode %q is invalid", sslmode)
	}
}

// validateProductionDBTLS validates the effective driver configuration so a
// raw DSN cannot bypass the production TLS policy.
func validateProductionDBTLS(cfg *DBConfig, production bool) error {
	if !production || cfg == nil || !cfg.Enabled {
		return nil
	}

	dsn, err := buildDSN(cfg)
	if err != nil {
		return errors.New("production database DSN is invalid")
	}
	dbType := strings.ToLower(strings.TrimSpace(cfg.Type))
	if dbType == "" {
		dbType = "mysql"
	}

	switch dbType {
	case "postgres", "postgresql":
		parsed, parseErr := pgconn.ParseConfig(dsn)
		if parseErr != nil {
			return errors.New("production PostgreSQL DSN is invalid")
		}
		if !postgresTLSVerifiesHostname(parsed) {
			return errors.New("production PostgreSQL requires sslmode=verify-full")
		}
	case "mysql":
		parsed, parseErr := mysqldriver.ParseDSN(dsn)
		if parseErr != nil {
			return errors.New("production MySQL DSN is invalid")
		}
		if parsed.TLS == nil || parsed.TLS.InsecureSkipVerify || parsed.AllowFallbackToPlaintext {
			return errors.New("production MySQL requires TLS with certificate verification")
		}
	default:
		return errors.New("production database type is unsupported")
	}
	return nil
}

func postgresTLSVerifiesHostname(cfg *pgconn.Config) bool {
	if cfg == nil || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify {
		return false
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback == nil || fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
			return false
		}
	}
	return true
}

// NewGormAdapter creates a database adapter using the compatibility startup contract.
func NewGormAdapter(cfg *DBConfig) (*GormAdapter, error) {
	return OpenGormAdapter(context.Background(), cfg)
}

// OpenGormAdapter creates and verifies a database adapter within ctx.
func OpenGormAdapter(ctx context.Context, cfg *DBConfig) (*GormAdapter, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("database startup canceled: %w", err)
	}
	validationCfg := cfg
	if cfg != nil && !cfg.Enabled {
		activeCfg := *cfg
		activeCfg.Enabled = true
		validationCfg = &activeCfg
	}
	if err := validateProductionDBTLS(validationCfg, isProductionMode(nil)); err != nil {
		return nil, err
	}
	dsn, err := buildDSN(cfg)
	if err != nil {
		return nil, err
	}

	dbType := cfg.Type
	if dbType == "" {
		dbType = "mysql"
	}
	dsn, err = databaseStartupDSN(ctx, dbType, dsn)
	if err != nil {
		return nil, err
	}

	var dialector gorm.Dialector
	switch dbType {
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	case "mysql", "":
		dialector = mysql.Open(dsn)
	case "sqlite", "sqlite3":
		dialector = sqlite.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}

	gormConfig := buildGormConfig(cfg)
	gormConfig.DisableAutomaticPing = true
	db, err := gorm.Open(dialector, gormConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	// 链路追踪集成 (已禁用 - 精简模式)

	// 注册全局回调
	if err := db.Callback().Create().Before("gorm:create").Register("bear:audit_create", auditCreateCallback); err != nil {
		ErrorLog("Failed to register audit create callback", "error", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("bear:audit_update", auditUpdateCallback); err != nil {
		ErrorLog("Failed to register audit update callback", "error", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	// 动态连接池配置
	maxIdle := 10
	if cfg.MaxIdleConns > 0 {
		maxIdle = cfg.MaxIdleConns
	}
	maxOpen := 100
	if cfg.MaxOpenConns > 0 {
		maxOpen = cfg.MaxOpenConns
	}
	maxLifetime := time.Hour
	if cfg.ConnMaxLifetime > 0 {
		maxLifetime = time.Duration(cfg.ConnMaxLifetime) * time.Minute
	}

	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetConnMaxLifetime(maxLifetime)

	Info("Database connected successfully",
		"type", cfg.Type,
		"host", cfg.Host,
		"port", cfg.Port,
		"dbname", cfg.DBName,
		"max_idle", maxIdle,
		"max_open", maxOpen)
	return &GormAdapter{DB: db}, nil
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

func buildGormConfig(cfg *DBConfig) *gorm.Config {
	slowThreshold := 200 * time.Millisecond
	if cfg != nil && cfg.SlowQueryThreshold != "" {
		if configured := parseDurationOrDefault(cfg.SlowQueryThreshold, 0); configured > 0 {
			slowThreshold = configured
		}
	}
	gormCfg := &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true, // 使用单数表名
		},
		PrepareStmt: false, // 禁用预编译语句缓存以兼容旧版驱动或某些插件
		Logger: newSecureGormLogger(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
			SlowThreshold:        slowThreshold,
			LogLevel:             logger.Warn,
			ParameterizedQueries: true,
		}),
	}
	return gormCfg
}

type secureGormLogger struct {
	output *log.Logger
	config logger.Config
}

func newSecureGormLogger(output *log.Logger, config logger.Config) logger.Interface {
	return &secureGormLogger{output: output, config: config}
}

func (l *secureGormLogger) LogMode(level logger.LogLevel) logger.Interface {
	cloned := *l
	cloned.config.LogLevel = level
	return &cloned
}

func (l *secureGormLogger) Info(context.Context, string, ...interface{}) {
	if l.config.LogLevel >= logger.Info {
		l.output.Print("gorm category=info")
	}
}

func (l *secureGormLogger) Warn(context.Context, string, ...interface{}) {
	if l.config.LogLevel >= logger.Warn {
		l.output.Print("gorm category=warn")
	}
}

func (l *secureGormLogger) Error(context.Context, string, ...interface{}) {
	if l.config.LogLevel >= logger.Error {
		l.output.Print("gorm category=error")
	}
}

func (l *secureGormLogger) Trace(_ context.Context, begin time.Time, query func() (string, int64), err error) {
	elapsed := time.Since(begin)
	category := ""
	switch {
	case err != nil && l.config.LogLevel >= logger.Error:
		category = "error"
	case l.config.SlowThreshold > 0 && elapsed > l.config.SlowThreshold && l.config.LogLevel >= logger.Warn:
		category = "slow"
	case l.config.LogLevel >= logger.Info:
		category = "trace"
	default:
		return
	}

	_, rows := query()
	l.output.Printf("gorm category=%s elapsed=%s rows=%d", category, elapsed, rows)
}

func (*secureGormLogger) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}

// Repository 基础仓库模式 (利用 Generics)
type Repository[T any] struct {
	Adapter *GormAdapter `inject:"-"`
}

func NewRepository[T any](adapter *GormAdapter) *Repository[T] {
	return &Repository[T]{Adapter: adapter}
}

func (r *Repository[T]) DB(ctx ...context.Context) *gorm.DB {
	adapter := r.Adapter
	if adapter == nil {
		adapter = GetByType[*GormAdapter]()
	}

	var db *gorm.DB
	var currentCtx context.Context

	if len(ctx) > 0 {
		currentCtx = ctx[0]
		// 1. 尝试从 gin.Context 中提取事务
		if ginCtx, ok := currentCtx.(*gin.Context); ok {
			if tx, exists := ginCtx.Get(txKey); exists {
				if gdb, ok := tx.(*gorm.DB); ok {
					db = gdb.WithContext(currentCtx)
				}
			}
		}
		if db == nil {
			db = adapter.DB.WithContext(currentCtx)
		}
	} else {
		db = adapter.DB
	}

	// 3. 多租户过滤 (已禁用 - 精简模式)

	return db
}

func (r *Repository[T]) Create(ctx context.Context, entity *T) error {
	return r.DB(ctx).Create(entity).Error
}

func (r *Repository[T]) FindByID(ctx context.Context, id interface{}) (*T, error) {
	var entity T
	err := r.DB(ctx).First(&entity, id).Error
	return &entity, err
}

// FindOne 根据条件查询单条数据，支持预加载关联模型
func (r *Repository[T]) FindOne(ctx context.Context, query any, preloads ...string) (*T, error) {
	db := r.DB(ctx)
	for _, p := range preloads {
		db = db.Preload(p)
	}

	var entity T
	err := db.Where(query).First(&entity).Error
	return &entity, err
}

// FindList 根据条件查询列表数据，支持预加载关联模型
func (r *Repository[T]) FindList(ctx context.Context, query any, preloads ...string) ([]*T, error) {
	db := r.DB(ctx)
	for _, p := range preloads {
		db = db.Preload(p)
	}

	var list []*T
	err := db.Where(query).Find(&list).Error
	return list, err
}

func (r *GormAdapter) Shutdown() error {
	if r == nil || r.DB == nil {
		return nil
	}
	r.shutdownOnce.Do(func() {
		sqlDB, err := r.DB.DB()
		if err != nil {
			r.shutdownErr = err
			return
		}
		Info("Closing database connection pool...")
		r.shutdownErr = sqlDB.Close()
	})
	return r.shutdownErr
}

func (*GormAdapter) lifecyclePrestarted() bool { return true }

func (r *GormAdapter) CheckReady(ctx context.Context) error {
	sqlDB, err := r.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// auditCreateCallback 审计创建回调
func auditCreateCallback(db *gorm.DB) {
	if db.Statement.Schema != nil {
		// 获取 userID (这里假设从 context 中获取，需要 context 传递规范)
		// 注意：GORM 的 ctx 是 db.Statement.Context
		userID := getUserIDFromContext(db.Statement.Context)
		if userID == "" {
			return
		}

		if model, ok := db.Statement.Dest.(AuditModel); ok {
			model.SetCreatedBy(userID)
			model.SetUpdatedBy(userID)
		}
	}
}

// auditUpdateCallback 审计更新回调
func auditUpdateCallback(db *gorm.DB) {
	if db.Statement.Schema != nil {
		userID := getUserIDFromContext(db.Statement.Context)
		if userID == "" {
			return
		}

		if model, ok := db.Statement.Dest.(AuditModel); ok {
			model.SetUpdatedBy(userID)
			return
		}
		if _, audited := db.Statement.Model.(AuditModel); audited {
			if _, ok := db.Statement.Dest.(map[string]interface{}); !ok {
				return
			}
			if field := db.Statement.Schema.LookUpField("UpdatedBy"); field != nil {
				db.Statement.SetColumn(field.DBName, userID)
			}
		}
	}
}

// getUserIDFromContext 尝试从上下文中提取用户 ID
func getUserIDFromContext(ctx context.Context) string {
	userID, _ := UserIDFromContext(ctx)
	return userID
}

// Update 更新实体，支持乐观锁
func (r *Repository[T]) Update(ctx context.Context, entity *T) error {
	db := r.DB(ctx)

	// 乐观锁检查
	if v, ok := any(entity).(VersionedModel); ok {
		currentVersion := v.GetVersion()
		v.SetVersion(currentVersion + 1)

		// 执行 CAS 更新: UPDATE table SET ..., version = currentVersion + 1 WHERE id = ? AND version = currentVersion
		// 注意：利用 ZeroValue 的更新特性，Version 是 int64，不会是零值。
		// GORM 的 Updates 默认更新所有非零值字段。
		// 为了安全，我们显式指定 WHERE 条件为旧版本号。
		result := db.Model(entity).Where("version = ?", currentVersion).Updates(entity)
		if result.Error != nil {
			v.SetVersion(currentVersion)
			return result.Error
		}
		if result.RowsAffected == 0 {
			v.SetVersion(currentVersion)
			return ErrOptimisticLock
		}
		return nil
	}

	result := db.Model(entity).Updates(entity)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Delete 删除实体
func (r *Repository[T]) Delete(ctx context.Context, entity *T) error {
	return r.DB(ctx).Delete(entity).Error
}

// FindUnscoped 查询包含已删除的数据 (软删除)
func (r *Repository[T]) FindUnscoped(ctx context.Context, id interface{}) (*T, error) {
	var entity T
	err := r.DB(ctx).Unscoped().First(&entity, id).Error
	return &entity, err
}

// Restore 恢复已删除的数据
func (r *Repository[T]) Restore(ctx context.Context, id interface{}) error {
	// GORM 恢复软删除通常是 Update DeletedAt = null
	var entity T
	return r.DB(ctx).Unscoped().Model(&entity).Where("id = ?", id).Update("deleted_at", nil).Error
}

// UpdateByID 根据 ID 更新（使用 map 避免全量更新）
func (r *Repository[T]) UpdateByID(ctx context.Context, id interface{}, updates map[string]interface{}) error {
	var entity T
	db := r.DB(ctx)
	clonedUpdates := make(map[string]interface{}, len(updates))
	for key, value := range updates {
		clonedUpdates[key] = value
	}
	result := db.Model(&entity).Where("id = ?", id).Updates(clonedUpdates)
	if result.Error != nil || result.RowsAffected > 0 {
		return result.Error
	}
	return gorm.ErrRecordNotFound
}

// DeleteByID 根据 ID 删除
func (r *Repository[T]) DeleteByID(ctx context.Context, id interface{}) error {
	var entity T
	return r.DB(ctx).Delete(&entity, id).Error
}

// Count 统计数量
func (r *Repository[T]) Count(ctx context.Context) (int64, error) {
	var entity T
	var count int64
	err := r.DB(ctx).Model(&entity).Count(&count).Error
	return count, err
}

// Exists 检查记录是否存在
func (r *Repository[T]) Exists(ctx context.Context, id interface{}) (bool, error) {
	var entity T
	var count int64
	err := r.DB(ctx).Model(&entity).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}
