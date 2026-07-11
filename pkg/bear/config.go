package bear

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
)

type UserConfig map[string]interface{}

type ServerConfig struct {
	Port                int32    `yaml:"port" json:"port" validate:"required,gt=0"`
	Name                string   `yaml:"name" json:"name" validate:"required"`
	Mode                string   `yaml:"mode" json:"mode"`
	TrustedProxies      []string `yaml:"trusted_proxies" json:"trusted_proxies"`
	HotReload           bool     `yaml:"hot_reload" json:"hot_reload"` // 是否开启热更新监听
	MachineID           int64    `yaml:"machine_id" json:"machine_id"` // 分布式 ID 机器码 (-1 为自动)
	ReadHeaderTimeout   string   `yaml:"read_header_timeout" json:"read_header_timeout"`
	ReadTimeout         string   `yaml:"read_timeout" json:"read_timeout"`
	WriteTimeout        string   `yaml:"write_timeout" json:"write_timeout"`
	IdleTimeout         string   `yaml:"idle_timeout" json:"idle_timeout"`
	ShutdownTimeout     string   `yaml:"shutdown_timeout" json:"shutdown_timeout"`
	MaxHeaderBytes      int      `yaml:"max_header_bytes" json:"max_header_bytes"`
	MaxRequestBodyBytes int64    `yaml:"max_request_body_bytes" json:"max_request_body_bytes"`
}

type HealthConfig struct {
	ReadinessTimeout string `yaml:"readiness_timeout" json:"readiness_timeout"`
}

type LogConfig struct {
	Level string `yaml:"level" json:"level"`
}

// Deprecated: WafConfig is compatibility-only and is not started.
type WafConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	StorageType string `yaml:"storage_type" json:"storage_type"`
	Redis       struct {
		Addr     string `yaml:"addr" json:"addr"`
		Password string `yaml:"password" json:"password"`
		DB       int    `yaml:"db" json:"db"`
	} `yaml:"redis" json:"redis"`
	Rules struct {
		MaxViolations          int `yaml:"max_violations" json:"max_violations"`
		ViolationWindowSeconds int `yaml:"violation_window_seconds" json:"violation_window_seconds"`
		BanDurationSeconds     int `yaml:"ban_duration_seconds" json:"ban_duration_seconds"`
	} `yaml:"rules" json:"rules"`
}

type CORSConfig struct {
	Enabled          bool     `yaml:"enabled" json:"enabled"`
	AllowOrigins     []string `yaml:"allow_origins" json:"allow_origins"`
	AllowMethods     []string `yaml:"allow_methods" json:"allow_methods"`
	AllowHeaders     []string `yaml:"allow_headers" json:"allow_headers"`
	AllowCredentials bool     `yaml:"allow_credentials" json:"allow_credentials"`
	MaxAge           string   `yaml:"max_age" json:"max_age"`
}

// Deprecated: GeoIPConfig is compatibility-only and is not loaded.
type GeoIPConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	CityMMDB string `yaml:"city_mmdb" json:"city_mmdb"`
}

type CronConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// MiddlewareConfig 中间件配置
type MiddlewareConfig struct {
	PerformanceLogLevel  string `yaml:"performance_log_level" json:"performance_log_level"`   // 日志级别: debug, info, warn, error
	SlowRequestThreshold string `yaml:"slow_request_threshold" json:"slow_request_threshold"` // 慢请求阈值，如 "1s"
}

type OpenAPIConfig struct {
	Enabled      bool         `yaml:"enabled" json:"enabled"`
	TimeWindow   int          `yaml:"time_window_seconds" json:"time_window_seconds"` // 默认 60 秒
	ReplayCheck  bool         `yaml:"replay_check" json:"replay_check"`
	Apps         []OpenAPIApp `yaml:"apps" json:"apps"`
	HeaderPrefix string       `yaml:"header_prefix" json:"header_prefix"` // X-API (X-API-Timestamp, X-API-Nonce)
}

type OpenAPIApp struct {
	AppKey    string `yaml:"app_key" json:"app_key"`
	AppSecret string `yaml:"app_secret" json:"app_secret"`
}

// Deprecated: BigQueryConfig is compatibility-only and is not started.
type BigQueryConfig struct {
	Enabled            bool   `yaml:"enabled" json:"enabled"`
	ProjectID          string `yaml:"project_id" json:"project_id"`
	DatasetID          string `yaml:"dataset_id" json:"dataset_id"`
	CredentialsPath    string `yaml:"credentials_path" json:"credentials_path"` // path to service account key file
	Credentials        string `yaml:"credentials" json:"credentials"`           // raw credentials json content or path, compatible with legacy
	Proxy              string `yaml:"proxy" json:"proxy"`
	BatchSize          int    `yaml:"batch_size" json:"batch_size"`
	BatchTimeout       int    `yaml:"batch_timeout_ms" json:"batch_timeout_ms"`
	MaxRetry           int    `yaml:"max_retry" json:"max_retry"`
	UseStorageWriteAPI bool   `yaml:"use_storage_write_api" json:"use_storage_write_api"`
	Async              bool   `yaml:"async" json:"async"`
}

// Deprecated: SchemaConfig is compatibility-only and is not loaded.
type SchemaConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	SchemaPath  string `yaml:"schema_path" json:"schema_path"` // directory containing json schemas
	StorageType string `yaml:"storage_type" json:"storage_type"`
	AutoReload  bool   `yaml:"auto_reload" json:"auto_reload"`
	FilePath    string `yaml:"file_path" json:"file_path"`
}

// Deprecated: KafkaConfig is compatibility-only and is not started.
type KafkaConfig struct {
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	Brokers      []string `yaml:"brokers" json:"brokers"`
	Topic        string   `yaml:"topic" json:"topic"`
	GroupID      string   `yaml:"group_id" json:"group_id"` // Added for consumer
	BatchSize    int      `yaml:"batch_size" json:"batch_size"`
	BatchTimeout int      `yaml:"batch_timeout_ms" json:"batch_timeout_ms"`
	Async        bool     `yaml:"async" json:"async"`
	Compression  string   `yaml:"compression" json:"compression"` // gzip, snappy, lz4, zstd
	AckPolicy    string   `yaml:"ack_policy" json:"ack_policy"`   // none, leader, all
	MaxRetries   int      `yaml:"max_retries" json:"max_retries"`
	SASL         struct {
		Enabled   bool   `yaml:"enabled" json:"enabled"`
		Mechanism string `yaml:"mechanism" json:"mechanism"` // plain, sha256, sha512
		User      string `yaml:"user" json:"user"`
		Password  string `yaml:"password" json:"password"`
	} `yaml:"sasl" json:"sasl"`
	TLS struct {
		Enabled            bool   `yaml:"enabled" json:"enabled"`
		CAFile             string `yaml:"ca_file" json:"ca_file"`
		CertFile           string `yaml:"cert_file" json:"cert_file"`
		KeyFile            string `yaml:"key_file" json:"key_file"`
		InsecureSkipVerify bool   `yaml:"insecure_skip_verify" json:"insecure_skip_verify"`
	} `yaml:"tls" json:"tls"`
}

type TracingConfig struct {
	Enabled      bool    `yaml:"enabled" json:"enabled"`
	ServiceName  string  `yaml:"service_name" json:"service_name"`
	Exporter     string  `yaml:"exporter" json:"exporter"`           // "stdout", "otlp"
	OTLPEndpoint string  `yaml:"otlp_endpoint" json:"otlp_endpoint"` // e.g., "http://localhost:4318"
	SampleRate   float64 `yaml:"sample_rate" json:"sample_rate"`     // 0.0 to 1.0
}

type I18nConfig struct {
	BundlePath      string   `yaml:"bundle_path" json:"bundle_path"`           // e.g., "locales"
	DefaultLanguage string   `yaml:"default_language" json:"default_language"` // e.g., "en"
	SupportedLangs  []string `yaml:"supported_langs" json:"supported_langs"`   // e.g., ["en", "zh"]
	Format          string   `yaml:"format" json:"format"`                     // e.g., "yaml"
}

type AuthConfig struct {
	StorageType      string   `yaml:"storage_type" json:"storage_type"`
	JWTSecret        string   `yaml:"jwt_secret" json:"jwt_secret" validate:"required_with=JWT"`
	JWTIssuer        string   `yaml:"jwt_issuer" json:"jwt_issuer"`
	JWTAudience      string   `yaml:"jwt_audience" json:"jwt_audience"`
	JWTClockSkew     string   `yaml:"jwt_clock_skew" json:"jwt_clock_skew"`
	TokenExpireHours int      `yaml:"token_expire_hours" json:"token_expire_hours"`
	PublicPaths      []string `yaml:"public_paths" json:"public_paths"`
}

type DBConfig struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	Type            string `yaml:"type" json:"type"`         // mysql, postgres (default: mysql)
	DSN             string `yaml:"dsn" json:"dsn"`           // 直接指定 DSN
	Host            string `yaml:"host" json:"host"`         // 主机
	User            string `yaml:"user" json:"user"`         // 用户名
	Password        string `yaml:"password" json:"password"` // 密码
	DBName          string `yaml:"dbname" json:"dbname"`     // 数据库名
	Port            string `yaml:"port" json:"port"`         // 端口
	PostgresSSLMode string `yaml:"postgres_sslmode" json:"postgres_sslmode"`
	TLS             string `yaml:"tls" json:"tls"` // MySQL driver TLS mode or registered config name
	// Deprecated: use PostgresSSLMode for PostgreSQL or TLS for MySQL.
	SSLMode            string `yaml:"sslmode" json:"sslmode"`
	MaxIdleConns       int    `yaml:"max_idle_conns" json:"max_idle_conns"`
	MaxOpenConns       int    `yaml:"max_open_conns" json:"max_open_conns"`
	ConnMaxLifetime    int    `yaml:"conn_max_lifetime_minutes" json:"conn_max_lifetime_minutes"`
	SlowQueryThreshold string `yaml:"slow_query_threshold" json:"slow_query_threshold"`
}

type MetricsConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Path    string `yaml:"path" json:"path"` // default: "/metrics"
}

type WebSocketConfig struct {
	HandshakeTimeout int      `yaml:"handshake_timeout_ms" json:"handshake_timeout_ms"`
	ReadBufferSize   int      `yaml:"read_buffer_size" json:"read_buffer_size"`
	WriteBufferSize  int      `yaml:"write_buffer_size" json:"write_buffer_size"`
	CheckOrigin      bool     `yaml:"check_origin" json:"check_origin"`
	AllowedOrigins   []string `yaml:"allowed_origins" json:"allowed_origins"`
}

// Deprecated: GRPCConfig is compatibility-only. Prefer the supported HTTP lifecycle.
type GRPCConfig struct {
	Enabled bool  `yaml:"enabled" json:"enabled"`
	Port    int32 `yaml:"port" json:"port"`
}

// Deprecated: CircuitBreakerConfig is compatibility-only and is not started.
type CircuitBreakerConfig struct {
	Enabled             bool    `yaml:"enabled" json:"enabled"`
	MaxRequests         uint32  `yaml:"max_requests" json:"max_requests"`                 // 半开状态下的最大请求数
	IntervalSeconds     int     `yaml:"interval_seconds" json:"interval_seconds"`         // 统计周期
	TimeoutSeconds      int     `yaml:"timeout_seconds" json:"timeout_seconds"`           // 熔断后多久进入半开状态
	ThresholdPercentage float64 `yaml:"threshold_percentage" json:"threshold_percentage"` // 错误率阈值 (0-1)
}

// Deprecated: ConfigCenterConfig is compatibility-only and is not loaded.
type ConfigCenterConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Type     string `yaml:"type" json:"type"`         // "redis", "etcd", "consul"
	Address  string `yaml:"address" json:"address"`   // Remote address
	Password string `yaml:"password" json:"password"` // Auth
	Key      string `yaml:"key" json:"key"`           // Config key e.g., "gin-bear:config"
	Format   string `yaml:"format" json:"format"`     // "yaml", "json"
}

type PluginConfig struct {
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	AllowedDirs []string `yaml:"allowed_dirs" json:"allowed_dirs"`
}

type SysConfig struct {
	Server         *ServerConfig         `yaml:"server" json:"server" validate:"required"`
	Auth           *AuthConfig           `yaml:"auth" json:"auth"`
	DB             *DBConfig             `yaml:"database" json:"database"` // 映射到 config.json 的 database
	Redis          *RedisConfig          `yaml:"redis" json:"redis"`
	Casbin         *CasbinConfig         `yaml:"casbin" json:"casbin"`
	CORS           *CORSConfig           `yaml:"cors" json:"cors"`
	Waf            *WafConfig            `yaml:"waf" json:"waf"`
	GeoIP          *GeoIPConfig          `yaml:"geoip" json:"geoip"`
	BigQuery       *BigQueryConfig       `yaml:"bigquery" json:"bigquery"`
	MQ             *MQConfig             `yaml:"mq" json:"mq"`
	Kafka          *KafkaConfig          `yaml:"kafka" json:"kafka"`
	RocketMQ       *RocketMQConfig       `yaml:"rocketmq" json:"rocketmq"`
	Pulsar         *PulsarConfig         `yaml:"pulsar" json:"pulsar"`
	Schema         *SchemaConfig         `yaml:"schema" json:"schema"`
	Tracing        *TracingConfig        `yaml:"tracing" json:"tracing"`
	I18n           *I18nConfig           `yaml:"i18n" json:"i18n"`
	Metrics        *MetricsConfig        `yaml:"metrics" json:"metrics"`
	Health         *HealthConfig         `yaml:"health" json:"health"`
	Log            *LogConfig            `yaml:"log" json:"log"`
	WS             *WebSocketConfig      `yaml:"websocket" json:"websocket"`
	GRPC           *GRPCConfig           `yaml:"grpc" json:"grpc"`
	CircuitBreaker *CircuitBreakerConfig `yaml:"circuit_breaker" json:"circuit_breaker"`
	ConfigCenter   *ConfigCenterConfig   `yaml:"config_center" json:"config_center"`
	Cron           *CronConfig           `yaml:"cron" json:"cron"`
	OpenAPI        *OpenAPIConfig        `yaml:"openapi" json:"openapi"`
	Swagger        *SwaggerConfig        `yaml:"swagger" json:"swagger"`
	Middleware     *MiddlewareConfig     `yaml:"middleware" json:"middleware"`
	Plugins        *PluginConfig         `yaml:"plugins" json:"plugins"`
	Config         UserConfig            `yaml:"config" json:"config"`
}

// Deprecated: MQConfig is compatibility-only and is not started.
type MQConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	Type       string `yaml:"type" json:"type"` // "kafka", "rocketmq", "pulsar", "noop"
	DlqPrefix  string `yaml:"dlq_prefix" json:"dlq_prefix"`
	DlqSuffix  string `yaml:"dlq_suffix" json:"dlq_suffix"`
	MaxRetries int    `yaml:"max_retries" json:"max_retries"`
}

// Deprecated: RocketMQConfig is compatibility-only and is not started.
type RocketMQConfig struct {
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	NameServers []string `yaml:"name_servers" json:"name_servers"`
	Topic       string   `yaml:"topic" json:"topic"`
	GroupName   string   `yaml:"group_name" json:"group_name"`
	RetryTimes  int      `yaml:"retry_times" json:"retry_times"` // Added
}

// Deprecated: PulsarConfig is compatibility-only and is not started.
type PulsarConfig struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`
	URL              string `yaml:"url" json:"url"`
	Topic            string `yaml:"topic" json:"topic"`
	Token            string `yaml:"token" json:"token"`
	SubscriptionName string `yaml:"subscription_name" json:"subscription_name"` // Added
}

type SwaggerConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Title    string `yaml:"title" json:"title"`
	Version  string `yaml:"version" json:"version"`
	Host     string `yaml:"host" json:"host"`
	BasePath string `yaml:"base_path" json:"base_path"`
}

func (c *SysConfig) Validate() error {
	validate := validator.New()
	if err := validate.Struct(c); err != nil {
		return err
	}
	return c.validateSemantic()
}

func (c *SysConfig) validateSemantic() error {
	if c == nil {
		return nil
	}
	if err := validateCORSConfig(c); err != nil {
		return err
	}
	if c.Tracing != nil {
		exporter := strings.ToLower(strings.TrimSpace(c.Tracing.Exporter))
		switch exporter {
		case "", "stdout", "console", "otlp", "otlphttp", "none", "noop":
		default:
			return fmt.Errorf("tracing.exporter must be one of stdout, otlp, none")
		}
		if c.Tracing.SampleRate < 0 || c.Tracing.SampleRate > 1 {
			return fmt.Errorf("tracing.sample_rate must be between 0 and 1")
		}
	}
	if c.Log != nil {
		switch strings.ToLower(strings.TrimSpace(c.Log.Level)) {
		case "", "debug", "info", "warn", "warning", "error":
		default:
			return fmt.Errorf("log.level must be one of debug, info, warn, error")
		}
	}
	if c.Metrics != nil && c.Metrics.Path != "" && !strings.HasPrefix(c.Metrics.Path, "/") {
		return fmt.Errorf("metrics.path must start with /")
	}
	if c.Server != nil {
		if c.Server.MaxHeaderBytes < 0 {
			return fmt.Errorf("server.max_header_bytes must not be negative")
		}
		if c.Server.MaxRequestBodyBytes < 0 {
			return fmt.Errorf("server.max_request_body_bytes must not be negative")
		}
		for name, value := range map[string]string{
			"server.read_header_timeout": c.Server.ReadHeaderTimeout,
			"server.read_timeout":        c.Server.ReadTimeout,
			"server.write_timeout":       c.Server.WriteTimeout,
			"server.idle_timeout":        c.Server.IdleTimeout,
			"server.shutdown_timeout":    c.Server.ShutdownTimeout,
		} {
			if value == "" {
				continue
			}
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("%s must be a valid duration: %w", name, err)
			}
		}
	}
	if c.Health != nil && c.Health.ReadinessTimeout != "" {
		if _, err := time.ParseDuration(c.Health.ReadinessTimeout); err != nil {
			return fmt.Errorf("health.readiness_timeout must be a valid duration: %w", err)
		}
	}
	if c.Auth != nil && c.Auth.JWTClockSkew != "" {
		skew, err := time.ParseDuration(c.Auth.JWTClockSkew)
		if err != nil {
			return fmt.Errorf("auth.jwt_clock_skew must be a valid duration: %w", err)
		}
		if skew < 0 || skew > 5*time.Minute {
			return fmt.Errorf("auth.jwt_clock_skew must be between 0 and 5m")
		}
	}
	if c.DB != nil {
		dbType := strings.ToLower(strings.TrimSpace(c.DB.Type))
		if dbType == "postgres" || dbType == "postgresql" {
			if _, err := effectivePostgresSSLMode(c.DB); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCORSConfig(config *SysConfig) error {
	if config == nil || config.CORS == nil || !config.CORS.Enabled || !config.CORS.AllowCredentials {
		return nil
	}
	for _, origin := range config.CORS.AllowOrigins {
		if origin == "*" {
			return fmt.Errorf("cors wildcard origin cannot be used with credentials")
		}
	}
	return nil
}

func (c *SysConfig) Name() string {
	return "SysConfig"
}

func (c *SysConfig) compatibilityWarnings() []string {
	if c == nil {
		return nil
	}

	var warnings []string
	seen := make(map[string]struct{})
	add := func(enabled bool, warning string) {
		if !enabled {
			return
		}
		if _, exists := seen[warning]; exists {
			return
		}
		seen[warning] = struct{}{}
		warnings = append(warnings, warning)
	}

	add(c.Waf != nil && c.Waf.Enabled, "waf is compatibility-only and is not started")
	add(c.GeoIP != nil && c.GeoIP.Enabled, "geoip is compatibility-only and is not loaded")
	add(c.BigQuery != nil && c.BigQuery.Enabled, "bigquery is compatibility-only and is not started")
	add(c.MQ != nil && c.MQ.Enabled, "mq is compatibility-only and is not started")
	add(c.Kafka != nil && c.Kafka.Enabled, "kafka is compatibility-only and is not started")
	add(c.RocketMQ != nil && c.RocketMQ.Enabled, "rocketmq is compatibility-only and is not started")
	add(c.Pulsar != nil && c.Pulsar.Enabled, "pulsar is compatibility-only and is not started")
	add(c.Schema != nil && c.Schema.Enabled, "schema is compatibility-only and is not loaded")
	add(c.CircuitBreaker != nil && c.CircuitBreaker.Enabled, "circuit_breaker is compatibility-only and is not started")
	add(c.ConfigCenter != nil && c.ConfigCenter.Enabled, "config_center is compatibility-only and is not loaded")
	if c.DB != nil {
		dbType := strings.ToLower(strings.TrimSpace(c.DB.Type))
		add((dbType == "" || dbType == "mysql") && strings.TrimSpace(c.DB.SSLMode) != "",
			"database.sslmode is ignored for MySQL; migrate to database.tls")
	}

	return warnings
}

// PostProcess 处理配置兼容性与默认值补全
func (c *SysConfig) PostProcess() {
	// 示例：BigQuery 兼容性处理
	if c.BigQuery != nil && c.BigQuery.Enabled {
		if c.BigQuery.CredentialsPath != "" && c.BigQuery.Credentials == "" {
			c.BigQuery.Credentials = c.BigQuery.CredentialsPath
		}
	}

	// 这里可以添加更多兼容性逻辑，例如 forwarders_v2 -> forwarders
	// 或者通过反射处理带有 alias 标签的字段
}

func NewSysConfig() *SysConfig {
	return &SysConfig{
		Server: &ServerConfig{
			Port:                8080,
			Name:                "gin-bear",
			MachineID:           -1,
			ReadHeaderTimeout:   "5s",
			ReadTimeout:         "15s",
			WriteTimeout:        "30s",
			IdleTimeout:         "60s",
			ShutdownTimeout:     "5s",
			MaxHeaderBytes:      1 << 20,
			MaxRequestBodyBytes: 1 << 20,
		},
		Auth: &AuthConfig{
			StorageType:      "file",
			JWTSecret:        "bear-secret",
			TokenExpireHours: 24,
			PublicPaths:      []string{"/health", "/live", "/ready", "/version", "/swagger/*", "/login"},
		},
		DB:     &DBConfig{Enabled: false, Type: "mysql", Host: "localhost", Port: "3306", User: "root"},
		Redis:  &RedisConfig{Addr: "localhost:6379", Password: "", DB: 0},
		Casbin: &CasbinConfig{},
		CORS: &CORSConfig{
			Enabled:      false,
			AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowHeaders: []string{"Content-Type", "Content-Length", "Accept-Encoding", "Authorization", "Accept", "Origin", "Cache-Control", "X-Requested-With", "X-Request-ID"},
			MaxAge:       "12h",
		},
		Waf:      &WafConfig{Enabled: false},
		GeoIP:    &GeoIPConfig{Enabled: false},
		BigQuery: &BigQueryConfig{Enabled: false},
		MQ:       &MQConfig{Enabled: false, Type: "noop", DlqSuffix: "_DLQ", MaxRetries: 3},
		Kafka:    &KafkaConfig{Enabled: false},
		RocketMQ: &RocketMQConfig{Enabled: false},
		Pulsar:   &PulsarConfig{Enabled: false},
		Schema:   &SchemaConfig{Enabled: false},
		Tracing: &TracingConfig{
			Enabled:     false,
			ServiceName: "gin-bear",
			Exporter:    "stdout",
			SampleRate:  1.0,
		},
		I18n: &I18nConfig{
			BundlePath:      "locales",
			DefaultLanguage: "en",
			SupportedLangs:  []string{"en", "zh"},
			Format:          "yaml",
		},
		Metrics: &MetricsConfig{
			Enabled: true,
			Path:    "/metrics",
		},
		Health: &HealthConfig{
			ReadinessTimeout: "3s",
		},
		Log: &LogConfig{
			Level: "info",
		},
		WS: &WebSocketConfig{
			HandshakeTimeout: 10000,
			ReadBufferSize:   1024,
			WriteBufferSize:  1024,
			CheckOrigin:      true,
		},
		Plugins: &PluginConfig{
			Enabled: false,
		},
		GRPC: &GRPCConfig{
			Enabled: false,
			Port:    9090,
		},
		CircuitBreaker: &CircuitBreakerConfig{
			Enabled:             false,
			MaxRequests:         5,
			IntervalSeconds:     10,
			TimeoutSeconds:      30,
			ThresholdPercentage: 0.5,
		},
		ConfigCenter: &ConfigCenterConfig{
			Enabled: false,
			Type:    "redis",
			Address: "localhost:6379",
			Key:     "gin-bear:config",
			Format:  "yaml",
		},
		OpenAPI: &OpenAPIConfig{
			Enabled:      false,
			TimeWindow:   60,
			ReplayCheck:  true,
			HeaderPrefix: "X-API",
			Apps:         []OpenAPIApp{{AppKey: "test", AppSecret: "test_secret"}},
		},
		Swagger: &SwaggerConfig{
			Enabled:  true,
			Title:    "Bear API",
			Version:  "1.0",
			Host:     "localhost:8080",
			BasePath: "/",
		},
		Middleware: &MiddlewareConfig{
			PerformanceLogLevel:  "info", // 日志级别: debug, info, warn, error
			SlowRequestThreshold: "1s",   // 慢请求阈值
		},
	}
}

func applyEnvOverrides(config *SysConfig) {
	if config == nil {
		return
	}
	if config.Server != nil {
		if portStr := os.Getenv("BEAR_SERVER_PORT"); portStr != "" {
			if p, err := strconv.Atoi(portStr); err == nil {
				config.Server.Port = int32(p)
				slog.Info("Config override by env", "BEAR_SERVER_PORT", p)
			}
		}
		if timeout := os.Getenv("BEAR_SHUTDOWN_TIMEOUT"); timeout != "" {
			config.Server.ShutdownTimeout = timeout
		}
	}
	if config.Auth != nil {
		if secret := os.Getenv("JWT_SECRET"); secret != "" {
			config.Auth.JWTSecret = secret
		}
	}
	if config.Redis != nil {
		if addr := os.Getenv("REDIS_ADDR"); addr != "" {
			config.Redis.Addr = addr
		}
		if required := os.Getenv("REDIS_REQUIRED"); required != "" {
			if v, err := strconv.ParseBool(required); err == nil {
				config.Redis.Required = v
			}
		}
	}
	if config.DB != nil {
		if host := os.Getenv("POSTGRES_HOST"); host != "" {
			config.DB.Host = host
		}
		if port := os.Getenv("POSTGRES_PORT"); port != "" {
			config.DB.Port = port
		}
		if user := os.Getenv("POSTGRES_USER"); user != "" {
			config.DB.User = user
		}
		if password := os.Getenv("POSTGRES_PASSWORD"); password != "" {
			config.DB.Password = password
		}
		if dbname := os.Getenv("POSTGRES_DB"); dbname != "" {
			config.DB.DBName = dbname
		}
		if maxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); maxOpen != "" {
			if v, err := strconv.Atoi(maxOpen); err == nil {
				config.DB.MaxOpenConns = v
			}
		}
		if maxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); maxIdle != "" {
			if v, err := strconv.Atoi(maxIdle); err == nil {
				config.DB.MaxIdleConns = v
			}
		}
	}
	if config.Health != nil {
		if timeout := os.Getenv("BEAR_READINESS_TIMEOUT"); timeout != "" {
			config.Health.ReadinessTimeout = timeout
		}
	}
	if config.Log != nil {
		if level := os.Getenv("LOG_LEVEL"); level != "" {
			config.Log.Level = level
		}
	}
	if config.Metrics != nil {
		if path := os.Getenv("METRICS_PATH"); path != "" {
			config.Metrics.Path = path
		}
	}
	if config.Tracing != nil {
		if exporter := os.Getenv("TRACING_EXPORTER"); exporter != "" {
			config.Tracing.Exporter = exporter
		}
		if endpoint := os.Getenv("TRACING_OTLP_ENDPOINT"); endpoint != "" {
			config.Tracing.OTLPEndpoint = endpoint
		}
	}
}
