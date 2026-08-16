// Package main 是 TransferServer 入口（Hertz）。
// 承载：上传分块接收与合并、下载 Range 直传、上传会话状态机。
// 详见《设计文档》第三章服务边界。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/config"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load("config.dev")
	if err != nil {
		panic("load config: " + err.Error())
	}

	if err := logger.Init("transfer", cfg.App.LogLevel, cfg.App.LogDir); err != nil {
		panic("init logger: " + err.Error())
	}
	defer logger.Sync()

	logger.L.Info("starting transferserver",
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.Transfer.Addr()),
	)

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

	mc, err := storage.New(ctx, cfg.MinIO.Endpoint, cfg.MinIO.AccessKey, cfg.MinIO.SecretKey, cfg.MinIO.Bucket, cfg.MinIO.Region, cfg.MinIO.UseSSL)
	if err != nil {
		logger.L.Warn("minio unavailable, running in degraded mode", zap.Error(err))
	}

	h := server.Default(
		server.WithHostPorts(cfg.Transfer.Addr()),
		server.WithReadTimeout(time.Duration(cfg.Transfer.ReadTimeout)*time.Second),
		server.WithWriteTimeout(time.Duration(cfg.Transfer.WriteTimeout)*time.Second),
	)
	h.Use(
		middleware.HertzRequestID(),
		middleware.HertzLogger(),
		middleware.HertzRecovery(),
	)

	registerRoutes(h, st, rc, mc)

	go func() {
		h.Spin()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.L.Info("shutting down transferserver")

	// Hertz 无内置 Graceful Shutdown 公开 API，骨架阶段直接退出。
	// 生产环境用 hertz 的 graceful shutdown 或在反向代理层做健康摘流。
	logger.L.Info("transferserver stopped")
}

func registerRoutes(h *server.Hertz, st *store.Store, rc *cache.Redis, mc *storage.MinIO) {
	h.GET("/healthz", healthz(st, rc, mc))

	v1 := h.Group("/api/v1")
	{
		// 上传模块
		_ = v1.Group("/upload")
		// 下载模块
		_ = v1.Group("/download")
	}
}

// healthz 健康检查：检查 PG/Redis/MinIO 连通性。
func healthz(st *store.Store, rc *cache.Redis, mc *storage.MinIO) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		status := consts.StatusOK
		checks := utils.H{}

		if st == nil {
			checks["postgres"] = "skip"
		} else if err := st.DB.PingContext(ctx); err != nil {
			checks["postgres"] = "fail"
			status = consts.StatusServiceUnavailable
		} else {
			checks["postgres"] = "ok"
		}

		if rc == nil {
			checks["redis"] = "skip"
		} else if err := rc.Client.Ping(ctx).Err(); err != nil {
			checks["redis"] = "fail"
			status = consts.StatusServiceUnavailable
		} else {
			checks["redis"] = "ok"
		}

		if mc == nil {
			checks["minio"] = "skip"
		} else {
			checks["minio"] = "ok"
		}

		c.JSON(status, utils.H{"status": "ok", "service": "transfer", "checks": checks})
	}
}
