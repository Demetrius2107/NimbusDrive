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

	"github.com/Demetrius2107/NimbusDrive/internal/auth"
	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/config"
	"github.com/Demetrius2107/NimbusDrive/internal/contract"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/handler"
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

	mc, err := storage.New(ctx, cfg.MinIO.Endpoint, cfg.MinIO.AccessKey, cfg.MinIO.SecretKey, cfg.MinIO.Bucket, cfg.MinIO.Region, cfg.MinIO.UseSSL, cfg.MinIO.PublicEndpoint, cfg.MinIO.PublicUseSSL)
	if err != nil {
		logger.L.Warn("minio unavailable, running in degraded mode", zap.Error(err))
	}

	jwtMgr := auth.New(cfg.JWT.Secret, cfg.JWT.AccessExpMin, cfg.JWT.RefreshExpDay, cfg.JWT.Issuer)

	// 契约注册表：启动期编译所有 JSON Schema，失败即 fatal。
	reg, err := contract.NewRegistry()
	if err != nil {
		logger.L.Fatal("contract registry init failed", zap.Error(err))
	}

	// 事件总线 Emitter：TransferServer 只生产事件，不消费。
	// Redis 不可用时降级为 no-op。关闭顺序（LIFO）：emitter → rc.Close（已 defer）。
	var emitter *eventbus.Emitter
	if rc != nil {
		emitter = eventbus.NewEmitter(rc.Client, cfg.EventBus.StreamPrefix, cfg.EventBus.BufferSize, cfg.EventBus.StreamMaxLen)
		defer emitter.Close()
	} else {
		logger.L.Warn("event bus disabled: redis unavailable")
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

	registerRoutes(h, st, rc, mc, jwtMgr, reg, emitter, cfg.MinIO)

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

func registerRoutes(h *server.Hertz, st *store.Store, rc *cache.Redis, mc *storage.MinIO, jwtMgr *auth.JWTManager, reg *contract.Registry, emitter *eventbus.Emitter, minioCfg config.MinIOConfig) {
	h.GET("/healthz", healthz(st, rc, mc))

	v1 := h.Group("/api/v1")

	// 上传模块：受 JWT 鉴权保护。
	upload := v1.Group("/upload", middleware.HertzJWTAuth(jwtMgr))
	if st != nil && mc != nil {
		uh := handler.NewUploadHandler(st.Repos(), mc, st.DB, emitter)
		upload.POST("/check-hash", middleware.HertzContract(reg, contract.UploadCheckHash), uh.CheckHash)
		upload.PUT("/:sessionId/chunks/:index", uh.UploadChunk)
		upload.GET("/:sessionId", uh.GetUploadStatus)
		upload.POST("/:sessionId/complete", uh.CompleteUpload)
		upload.DELETE("/:sessionId", uh.CancelUpload)
	} else {
		logger.L.Warn("upload routes disabled: store or minio unavailable")
	}

	// 下载模块：受 JWT 鉴权保护。
	download := v1.Group("/download", middleware.HertzJWTAuth(jwtMgr))
	if st != nil && mc != nil {
		dh := handler.NewDownloadHandler(st.Repos(), mc, minioCfg.PresignExpireSec)
		// presign 路由必须在 /:fileId 之前注册，避免被通配路由吞掉。
		download.GET("/:fileId/presign", dh.Presign)
		download.GET("/:fileId", dh.Download)
		download.HEAD("/:fileId", dh.Head)
	} else {
		logger.L.Warn("download routes disabled: store or minio unavailable")
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
