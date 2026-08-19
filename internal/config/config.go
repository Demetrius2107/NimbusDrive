// Package config 加载并管理 NimbusDrive 的运行时配置。
// 两服务（APIServer/TransferServer）共享同一份配置 schema，按需读取各自字段。
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config 是全局配置根。
type Config struct {
	App            AppConfig            `mapstructure:"app"`
	APIServer      ServerConfig         `mapstructure:"api_server"`
	Transfer       ServerConfig         `mapstructure:"transfer_server"`
	Postgres       PostgresConfig       `mapstructure:"postgres"`
	Redis          RedisConfig          `mapstructure:"redis"`
	MinIO          MinIOConfig          `mapstructure:"minio"`
	JWT            JWTConfig            `mapstructure:"jwt"`
	EventBus       EventBusConfig       `mapstructure:"event_bus"`
	Observability  ObservabilityConfig  `mapstructure:"observability"`
}

type AppConfig struct {
	Name        string `mapstructure:"name"`
	Env         string `mapstructure:"env"` // dev | prod
	LogLevel    string `mapstructure:"log_level"`
	LogDir      string `mapstructure:"log_dir"`
	StorageMode string `mapstructure:"storage_mode"` // minio | oss
}

// ServerConfig 描述单个服务的监听与运行参数。
type ServerConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	ReadTimeout  int    `mapstructure:"read_timeout_sec"`
	WriteTimeout int    `mapstructure:"write_timeout_sec"`
}

func (s ServerConfig) Addr() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }

type PostgresConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Database string `mapstructure:"database"`
	SSLMode  string `mapstructure:"ssl_mode"`
	MaxOpen  int    `mapstructure:"max_open_conns"`
	MaxIdle  int    `mapstructure:"max_idle_conns"`
}

// DSN 返回 pgx 驱动可用的连接串。
func (p PostgresConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		p.Host, p.Port, p.User, p.Password, p.Database, p.SSLMode,
	)
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

func (r RedisConfig) Addr() string { return fmt.Sprintf("%s:%d", r.Host, r.Port) }

type MinIOConfig struct {
	Endpoint         string `mapstructure:"endpoint"`
	AccessKey        string `mapstructure:"access_key"`
	SecretKey        string `mapstructure:"secret_key"`
	Bucket           string `mapstructure:"bucket"`
	UseSSL           bool   `mapstructure:"use_ssl"`
	Region           string `mapstructure:"region"`
	PresignExpireSec int    `mapstructure:"presign_expire_sec"`
	// PublicEndpoint 是客户端可达的 MinIO 端点，用于签发预签名下载 URL。
	// 内部上传走 Endpoint（内网），预签名 URL 必须用客户端可达端点。
	// 为空时回退到 Endpoint（dev 环境客户端直连内网 MinIO）。
	PublicEndpoint string `mapstructure:"public_endpoint"`
	PublicUseSSL   bool   `mapstructure:"public_use_ssl"`
	// ShareDownloadTokenTTLSec 是分享下载能力令牌的 TTL（秒）。
	// 令牌单次消费，TTL 仅约束未兑换令牌的存活时间。
	ShareDownloadTokenTTLSec int `mapstructure:"share_download_token_ttl_sec"`
}

type JWTConfig struct {
	Secret        string `mapstructure:"secret"`
	AccessExpMin  int    `mapstructure:"access_exp_min"`
	RefreshExpDay int    `mapstructure:"refresh_exp_day"`
	Issuer        string `mapstructure:"issuer"`
}

// EventBusConfig 描述 Redis Streams 事件总线参数。
type EventBusConfig struct {
	StreamPrefix  string `mapstructure:"stream_prefix"`   // stream 名前缀，如 "nimbus:events:"
	ConsumerGroup string `mapstructure:"consumer_group"`  // 消费者组名
	BufferSize    int    `mapstructure:"buffer_size"`     // Emitter channel 缓冲大小
	MaxRetries    int    `mapstructure:"max_retries"`     // 毒丸阈值：超过此次数进 DLQ
	BlockMs       int    `mapstructure:"block_ms"`        // XREADGROUP block 毫秒
	DLQPrefix     string `mapstructure:"dlq_prefix"`      // 死信队列 stream 前缀
	StreamMaxLen  int64  `mapstructure:"stream_max_len"`  // 每条 stream 近似上限（XADD MAXLEN ~）
}

// ObservabilityConfig 描述可观测性参数（追踪 + 指标）。
type ObservabilityConfig struct {
	// Exporter trace 导出方式：stdout（默认，写 stderr）| otlp（gRPC 推 collector）| none（no-op 降级）。
	Exporter string `mapstructure:"exporter"`
	// OTLPEndpoint OTLP gRPC 端点，如 localhost:4317。仅 exporter=otlp 时生效。
	OTLPEndpoint string `mapstructure:"otlp_endpoint"`
	// ServiceName 覆盖默认服务名（默认按二进制：api/transfer）。
	ServiceName string `mapstructure:"service_name"`
	// SampleRatio 采样率 0-1，1.0=全采样。用 ParentBased(TraceIDRatioBased) 策略。
	SampleRatio float64 `mapstructure:"sample_ratio"`
	// MetricsEnabled 是否启用 Prometheus 指标 + /metrics 端点。默认 true。
	MetricsEnabled bool `mapstructure:"metrics_enabled"`
	// MetricsPath Prometheus 抓取路径，默认 /metrics。
	MetricsPath string `mapstructure:"metrics_path"`
}

// Load 从 configs/ 目录读取指定名称的 yaml，并叠加同名环境变量覆盖。
// name 不含扩展名，如 "config.dev"。
func Load(name string) (*Config, error) {
	v := viper.New()
	v.SetConfigName(name)
	v.SetConfigType("yaml")
	v.AddConfigPath("./configs")
	v.AddConfigPath("./")

	// 环境变量覆盖：NIMBUS_APP_NAME -> app.name
	v.SetEnvPrefix("NIMBUS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 事件总线默认值（yaml 未配置时仍可用）
	v.SetDefault("event_bus.stream_prefix", "nimbus:events:")
	v.SetDefault("event_bus.consumer_group", "nimbus-workers")
	v.SetDefault("event_bus.buffer_size", 1024)
	v.SetDefault("event_bus.max_retries", 3)
	v.SetDefault("event_bus.block_ms", 2000)
	v.SetDefault("event_bus.dlq_prefix", "nimbus:dlq:")
	v.SetDefault("event_bus.stream_max_len", 10000)

	// MinIO 预签名下载默认值
	v.SetDefault("minio.presign_expire_sec", 3600)
	v.SetDefault("minio.share_download_token_ttl_sec", 300)

	// 可观测性默认值（stdout 导出 + 全采样，开发期零外部依赖）
	v.SetDefault("observability.exporter", "stdout")
	v.SetDefault("observability.otlp_endpoint", "localhost:4317")
	v.SetDefault("observability.sample_ratio", 1.0)
	// 指标默认启用 + /metrics 路径
	v.SetDefault("observability.metrics_enabled", true)
	v.SetDefault("observability.metrics_path", "/metrics")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", name, err)
	}

	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &c, nil
}
