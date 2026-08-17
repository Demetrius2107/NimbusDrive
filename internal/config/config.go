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
	App       AppConfig       `mapstructure:"app"`
	APIServer ServerConfig    `mapstructure:"api_server"`
	Transfer  ServerConfig    `mapstructure:"transfer_server"`
	Postgres  PostgresConfig  `mapstructure:"postgres"`
	Redis     RedisConfig     `mapstructure:"redis"`
	MinIO     MinIOConfig     `mapstructure:"minio"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	EventBus  EventBusConfig  `mapstructure:"event_bus"`
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
	Endpoint        string `mapstructure:"endpoint"`
	AccessKey       string `mapstructure:"access_key"`
	SecretKey       string `mapstructure:"secret_key"`
	Bucket          string `mapstructure:"bucket"`
	UseSSL          bool   `mapstructure:"use_ssl"`
	Region          string `mapstructure:"region"`
	PresignExpireSec int   `mapstructure:"presign_expire_sec"`
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

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", name, err)
	}

	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &c, nil
}
