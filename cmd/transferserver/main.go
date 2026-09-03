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
	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func main() {
	// 配置名可用 NIMBUS_CONFIG_NAME 覆盖（容器部署用 config.prod），默认开发配置。
	cfgName := os.Getenv("NIMBUS_CONFIG_NAME")
	if cfgName == "" {
		cfgName = "config.dev"
	}
	cfg, err := config.Load(cfgName)
	if err != nil {
		panic("load config: " + err.Error())
	}

	if err := logger.Init("transfer", cfg.App.LogLevel, cfg.App.LogDir); err != nil {
		panic("init logger: " + err.Error())
	}
	defer logger.Sync()

	// 分布式追踪：TracerProvider 在 logger 之后初始化，defer Shutdown 先于 logger.Sync。
	serviceName := cfg.Observability.ServiceName
	if serviceName == "" {
		serviceName = "transfer"
	}
	tp, err := tracing.Init(serviceName, cfg.Observability.Exporter, cfg.Observability.OTLPEndpoint, cfg.Observability.SampleRatio)
	if err != nil {
		logger.L.Warn("tracer init failed, tracing disabled", zap.Error(err))
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tracing.Shutdown(shutdownCtx, tp)
		}()
	}

	// Prometheus 指标：紧跟 tracing 初始化（exemplar 需读 span context）。
	// 变量名用 metricCollector 避免与下方 mc（*storage.MinIO）冲突。
	metricCollector := metrics.Init(serviceName)
	defer metrics.Shutdown()

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

	// 注册 infra 指标 collector（TransferServer 无 consumer/logAggregator，只注入 emitter）。
	metricCollector.RegisterInfra(metrics.InfraSources{
		Emitter: emitter,
	})

	h := server.Default(
		server.WithHostPorts(cfg.Transfer.Addr()),
		server.WithReadTimeout(time.Duration(cfg.Transfer.ReadTimeout)*time.Second),
		server.WithWriteTimeout(time.Duration(cfg.Transfer.WriteTimeout)*time.Second),
	)
	h.Use(
		middleware.HertzRequestID(),
		middleware.HertzTracer("transferserver"),
		middleware.HertzMetrics("transfer"),
		middleware.HertzLogger(),
		middleware.HertzRecovery(),
	)

	// /metrics 端点：与 /healthz 同级，无 JWT。EnableOpenMetrics 协商以支持 exemplar。
	// Hertz 与 net/http 类型不互通，用 common/adaptor 适配 Request/ResponseWriter。
	if cfg.Observability.MetricsEnabled {
		metricsHandler := promhttp.HandlerFor(metricCollector.Registry, promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		})
		h.GET(cfg.Observability.MetricsPath, func(ctx context.Context, c *app.RequestContext) {
			req, err := adaptor.GetCompatRequest(&c.Request)
			if err != nil {
				c.AbortWithStatus(consts.StatusInternalServerError)
				return
			}
			metricsHandler.ServeHTTP(adaptor.GetCompatResponseWriter(&c.Response), req)
		})
	}

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
		outboxRepo := store.NewOutboxRepo(st.DB)
		uh := handler.NewUploadHandler(st.Repos(), mc, st.DB, emitter, outboxRepo)
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

	// 分享下载兑换：公开端点，无 JWT。凭能力令牌兑换预签名直连 URL。
	// 三依赖齐全才注册：rc 兑换令牌、st 查文件、mc 签 URL。
	if st != nil && mc != nil && rc != nil {
		sdh := handler.NewShareDownloadHandler(st.Repos(), mc, rc, minioCfg.PresignExpireSec)
		v1.GET("/s/download/:token", sdh.Redeem)
	} else {
		logger.L.Warn("share download route disabled: store/minio/redis unavailable")
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
