// Package main 是 APIServer 入口（Gin）。
// 承载：用户/鉴权、管理端、配额、文件元数据 CRUD、分享、文件列表、回收站。
// 详见《设计文档》第三章服务边界。
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/adminstore"
	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/config"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load("config.dev")
	if err != nil {
		// 配置加载失败时尚无 logger，直接输出到 stderr。
		panic("load config: " + err.Error())
	}

	if err := logger.Init("api", cfg.App.LogLevel, cfg.App.LogDir); err != nil {
		panic("init logger: " + err.Error())
	}
	defer logger.Sync()

	logger.L.Info("starting apiserver",
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.APIServer.Addr()),
	)

	// 依赖初始化。骨架阶段不强制要求 PG/Redis/MinIO 在线：
	// 连接失败仅告警并继续启动，使 /healthz 与路由骨架可独立验证。
	// 真正实现接口时改为强依赖。
	ctx := context.Background()
	st, err := store.New(ctx, cfg.Postgres.DSN(), cfg.Postgres.MaxOpen, cfg.Postgres.MaxIdle)
	if err != nil {
		logger.L.Warn("postgres unavailable, running in degraded mode", zap.Error(err))
	}
	defer func() { _ = st.Close() }()

	rc, err := cache.New(ctx, cfg.Redis.Addr(), cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		logger.L.Warn("redis unavailable, running in degraded mode", zap.Error(err))
	}
	defer func() { _ = rc.Close() }()

	adb, err := adminstore.New(ctx, cfg.Postgres.DSN(), cfg.App.LogLevel)
	if err != nil {
		logger.L.Warn("gorm unavailable, running in degraded mode", zap.Error(err))
	}
	defer func() { _ = adb.Close() }()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		middleware.GinRequestID(),
		middleware.GinLogger(),
		middleware.GinRecovery(),
	)

	registerRoutes(r, st, rc, adb)

	srv := &http.Server{
		Addr:         cfg.APIServer.Addr(),
		Handler:      r,
		ReadTimeout:  time.Duration(cfg.APIServer.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.APIServer.WriteTimeout) * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L.Fatal("apiserver listen", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.L.Info("shutting down apiserver")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.L.Error("apiserver shutdown", zap.Error(err))
	}
	logger.L.Info("apiserver stopped")
}

func registerRoutes(r *gin.Engine, st *store.Store, rc *cache.Redis, adb *adminstore.DB) {
	r.GET("/healthz", healthz(st, rc))

	v1 := r.Group("/api/v1")
	{
		// 鉴权模块
		_ = v1.Group("/auth")
		// 用户模块
		_ = v1.Group("/users")
		// 文件元数据模块
		_ = v1.Group("/files")
		// 分享模块
		_ = v1.Group("/shares")
		// 管理端模块
		_ = v1.Group("/admin")
	}
}

// healthz 健康检查：检查 PG 与 Redis 连通性。
func healthz(st *store.Store, rc *cache.Redis) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := http.StatusOK
		checks := gin.H{}

		if st == nil {
			checks["postgres"] = "skip"
		} else if err := st.DB.PingContext(c.Request.Context()); err != nil {
			checks["postgres"] = "fail"
			status = http.StatusServiceUnavailable
		} else {
			checks["postgres"] = "ok"
		}

		if rc == nil {
			checks["redis"] = "skip"
		} else if err := rc.Client.Ping(c.Request.Context()).Err(); err != nil {
			checks["redis"] = "fail"
			status = http.StatusServiceUnavailable
		} else {
			checks["redis"] = "ok"
		}

		c.JSON(status, gin.H{"status": "ok", "service": "api", "checks": checks})
	}
}
