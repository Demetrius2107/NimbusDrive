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
	"github.com/Demetrius2107/NimbusDrive/internal/auth"
	"github.com/Demetrius2107/NimbusDrive/internal/cache"
	"github.com/Demetrius2107/NimbusDrive/internal/config"
	"github.com/Demetrius2107/NimbusDrive/internal/contract"
	"github.com/Demetrius2107/NimbusDrive/internal/cron"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus"
	"github.com/Demetrius2107/NimbusDrive/internal/eventbus/consumers"
	"github.com/Demetrius2107/NimbusDrive/internal/handler"
	"github.com/Demetrius2107/NimbusDrive/internal/logger"
	"github.com/Demetrius2107/NimbusDrive/internal/metrics"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/Demetrius2107/NimbusDrive/internal/quota"
	"github.com/Demetrius2107/NimbusDrive/internal/storage"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/Demetrius2107/NimbusDrive/internal/tracing"
	"github.com/gin-gonic/gin"
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
		// 配置加载失败时尚无 logger，直接输出到 stderr。
		panic("load config: " + err.Error())
	}

	if err := logger.Init("api", cfg.App.LogLevel, cfg.App.LogDir); err != nil {
		panic("init logger: " + err.Error())
	}
	defer logger.Sync()

	// 分布式追踪：TracerProvider 在 logger 之后初始化（tracing 可能记日志），
	// 在 logger.Sync 之前 defer Shutdown（LIFO：trace 先 flush 再 sync 日志）。
	serviceName := cfg.Observability.ServiceName
	if serviceName == "" {
		serviceName = "api"
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
	// metrics_enabled=false 时仍 Init（Default 返回 noop，/metrics 不挂载）。
	mc := metrics.Init(serviceName)
	defer metrics.Shutdown()

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

	// 操作日志聚合器：admin 操作 + 事件驱动审计共用。
	// 在事件总线之前构造（审计消费者依赖它），关闭顺序在事件总线之后。
	var la *adminstore.LogAggregator
	if adb != nil {
		if err := adb.Migrate(ctx); err != nil {
			logger.L.Warn("admin migrate failed", zap.Error(err))
		}
		la = adminstore.NewLogAggregator(adb.GORM, 1024, 50, 5*time.Second)
		defer la.Close()
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		middleware.GinRequestID(),
		middleware.GinTracer("apiserver"),
		middleware.GinMetrics("api"),
		middleware.GinLogger(),
		middleware.GinRecovery(),
	)

	jwtMgr := auth.New(cfg.JWT.Secret, cfg.JWT.AccessExpMin, cfg.JWT.RefreshExpDay, cfg.JWT.Issuer)

	// 契约注册表：启动期编译所有 JSON Schema，失败即 fatal。
	reg, err := contract.NewRegistry()
	if err != nil {
		logger.L.Fatal("contract registry init failed", zap.Error(err))
	}

	// 事件总线：Redis 可用时启动 Emitter + 消费者。
	// 关闭顺序（LIFO）：消费者 → emitter → la.Close → rc.Close（均 defer）。
	var emitter *eventbus.Emitter
	var consumerClosers []func()
	var builtConsumers []*eventbus.Consumer // 保留引用供 metrics infra collector scrape
	if rc != nil {
		emitter = eventbus.NewEmitter(rc.Client, cfg.EventBus.StreamPrefix, cfg.EventBus.BufferSize, cfg.EventBus.StreamMaxLen)
		defer emitter.Close() // 在消费者 Close 之后执行

		consumerCfg := consumers.Config{
			StreamPrefix:  cfg.EventBus.StreamPrefix,
			ConsumerGroup: cfg.EventBus.ConsumerGroup,
			MaxRetries:    cfg.EventBus.MaxRetries,
			BlockMs:       cfg.EventBus.BlockMs,
			DLQPrefix:     cfg.EventBus.DLQPrefix,
		}
		// 5 个消费者：审计（需 LogAggregator）、上传统计、传输配额、配额对账、no-op 骨架
		builders := []struct {
			name    string
			builder consumers.ConsumerBuilder
		}{
			{"api-audit", consumers.NewAuditConsumer(la)},
			{"api-upload-stats", consumers.NewUploadStatsConsumer(rc.Client)},
			{"api-quota", consumers.NewQuotaConsumer(st.DB)},
			{"api-quota-reconcile", consumers.NewQuotaReconcileConsumer(st.DB, rc.Client)},
			{"api-noop", consumers.NewNoopConsumer()},
		}
		for _, b := range builders {
			c := consumers.Build(rc.Client, consumerCfg, b.builder, b.name)
			if err := c.Start(); err != nil {
				logger.L.Error("consumer start failed", zap.String("name", b.name), zap.Error(err))
				continue
			}
			builtConsumers = append(builtConsumers, c)
			consumerClosers = append(consumerClosers, c.Close)
		}
		// 消费者最先关闭（后注册先执行，在 emitter.Close 之前）
		defer func() {
			for _, closeFn := range consumerClosers {
				closeFn()
			}
		}()
	} else {
		logger.L.Warn("event bus disabled: redis unavailable")
	}

	// 注册 infra 指标 collector（scrape-time 查 emitter/consumer/logAggregator）。
	// 各源可空（Redis/PG 不可用时降级跳过对应指标）。
	var multiConsumer metrics.ConsumerMetrics
	if len(builtConsumers) > 0 {
		csms := make([]metrics.ConsumerMetrics, len(builtConsumers))
		for i, c := range builtConsumers {
			csms[i] = c
		}
		multiConsumer = metrics.NewMultiConsumer(csms...)
	}
	mc.RegisterInfra(metrics.InfraSources{
		Emitter:  emitter,
		Consumer: multiConsumer,
		LogAgg:   la,
	})

	// 配额实时推送：Redis 可用时构造 Notifier + SSE handler + 月度重置 cron。
	var notifier *quota.Notifier
	var sseHandler *quota.SSEHandler
	var monthlyCronStop func()
	if rc != nil {
		notifier = quota.NewNotifier(rc.Client)
		if st != nil {
			sseHandler = quota.NewSSEHandler(notifier, st.Repos().Quotas, st.Repos().Users)
			// 月度配额重置 cron：进程内每 6h 检查月初，为活跃用户批量建当月行。
			resetCron := quota.NewMonthlyResetCron(st.Repos().Quotas)
			monthlyCronStop = resetCron.Start(ctx)
			defer monthlyCronStop()
		}
	} else {
		logger.L.Warn("quota streaming disabled: redis unavailable")
	}

	// MinIO 客户端：APIServer 控制面用（hash GC 回收物理对象）。
	// storage 包注释已预期 APIServer 使用 MinIO；与 TransferServer 同配置。
	// PG+MinIO 齐全时装配 4 个清理/relay cron（crons 全部跑在 APIServer 控制面）。
	// 变量名 minioCli 避开上方 metrics 收集器 mc（line 67）。
	var minioCli *storage.MinIO
	var outboxRepo *store.OutboxRepo
	if st != nil {
		minioCli, err = storage.New(ctx, cfg.MinIO.Endpoint, cfg.MinIO.AccessKey, cfg.MinIO.SecretKey, cfg.MinIO.Bucket, cfg.MinIO.Region, cfg.MinIO.UseSSL, cfg.MinIO.PublicEndpoint, cfg.MinIO.PublicUseSSL)
		if err != nil {
			logger.L.Warn("minio unavailable, GC crons disabled", zap.Error(err))
			minioCli = nil
		}
		outboxRepo = store.NewOutboxRepo(st.DB)
	}
	if st != nil && minioCli != nil {
		repos := st.Repos()
		// 回收站 30 天物理清理 cron。
		trashCron := cron.NewTrashPurgeCron(repos.Files, repos.Hashes, repos.Users, st.DB,
			cfg.Cron.TrashPurge.RetentionDays, cfg.Cron.TrashPurge.BatchSize, cfg.Cron.TrashPurge.IntervalSec)
		defer trashCron.Start(ctx)()
		// 零引用哈希 GC cron（tombstone + grace + MinIO 回收）。
		hashGCCron := cron.NewHashGCCron(repos.Hashes, minioCli, st.DB,
			cfg.Cron.HashGC.GraceHours, cfg.Cron.HashGC.BatchSize, cfg.Cron.HashGC.IntervalSec)
		defer hashGCCron.Start(ctx)()
		// 上传会话过期清理 cron。
		sessionCron := cron.NewSessionExpiryCron(repos.Uploads, repos.Files, minioCli,
			cfg.Cron.SessionExpiry.BatchSize, cfg.Cron.SessionExpiry.IntervalSec)
		defer sessionCron.Start(ctx)()
	}
	// outbox relay：PG+emitter 齐全即装配（不依赖 MinIO）。
	if st != nil && emitter != nil {
		relay := cron.NewOutboxRelay(outboxRepo, emitter, st.DB,
			cfg.Cron.Outbox.BatchSize, cfg.Cron.Outbox.IntervalSec)
		defer relay.Start(ctx)()
	}
	// /metrics 端点：无 JWT 保护（与 /healthz 同级），生产用反向代理/网络隔离保护。
	// EnableOpenMetrics 协商：Prometheus 2.5+ 优先用 OpenMetrics，exemplar 仅此格式可传。
	if cfg.Observability.MetricsEnabled {
		r.GET(cfg.Observability.MetricsPath, gin.WrapH(promhttp.HandlerFor(mc.Registry, promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		})))
	}

	registerRoutes(r, st, rc, adb, jwtMgr, reg, emitter, la, notifier, sseHandler, outboxRepo, cfg.MinIO)

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

func registerRoutes(r *gin.Engine, st *store.Store, rc *cache.Redis, adb *adminstore.DB, jwtMgr *auth.JWTManager, reg *contract.Registry, emitter *eventbus.Emitter, la *adminstore.LogAggregator, notifier *quota.Notifier, sseHandler *quota.SSEHandler, outboxRepo *store.OutboxRepo, minioCfg config.MinIOConfig) {
	r.GET("/healthz", healthz(st, rc))

	v1 := r.Group("/api/v1")
	{
		// 鉴权模块：register/login 公开，me 需鉴权
		if st != nil {
			authHandler := handler.NewAuthHandler(st.Repos().Users, jwtMgr, emitter)
			authGrp := v1.Group("/auth")
			{
				authGrp.POST("/register", authHandler.Register)
				authGrp.POST("/login", authHandler.Login)
				authGrp.GET("/me", middleware.GinJWTAuth(jwtMgr), authHandler.Me)
			}
		} else {
			logger.L.Warn("postgres unavailable, /auth routes disabled")
		}

		// 用户模块
		_ = v1.Group("/users")

		// 文件管理模块 + 回收站 + 分享创建：受 JWT 鉴权保护。
		if st != nil {
			fh := handler.NewFileHandler(st.Repos().Files, st.Repos().Hashes, st.DB)
			filesGrp := v1.Group("/files", middleware.GinJWTAuth(jwtMgr))
			{
				filesGrp.GET("", fh.List)
				filesGrp.POST("/folder", fh.CreateFolder)
				filesGrp.POST("/:id/move", fh.Move)
				filesGrp.POST("/:id/rename", fh.Rename)
				filesGrp.DELETE("/:id", fh.SoftDelete)
			}
			trashGrp := v1.Group("/trash", middleware.GinJWTAuth(jwtMgr))
			{
				trashGrp.GET("", fh.ListTrash)
				trashGrp.POST("/:id/restore", fh.Restore)
				trashGrp.DELETE("/:id", fh.PermanentDelete)
			}

			// 分享模块：创建挂在 /files/:id/share，管理挂在 /shares，公开访问挂在 /s
			sh := handler.NewShareHandler(st.Repos().Shares, st.Repos().Files, rc, st.DB, emitter, time.Duration(minioCfg.ShareDownloadTokenTTLSec)*time.Second)
			filesGrp.POST("/:id/share", middleware.GinContract(reg, contract.ShareCreate), sh.CreateShare)
			sharesGrp := v1.Group("/shares", middleware.GinJWTAuth(jwtMgr))
			{
				sharesGrp.GET("", sh.ListShares)
				sharesGrp.DELETE("/:id", sh.CancelShare)
			}
			// 公开访问（无 JWT）
			pubGrp := v1.Group("/s")
			{
				pubGrp.GET("/:id", sh.GetShare)
				pubGrp.POST("/:id/validate", middleware.GinContract(reg, contract.ShareValidate), sh.ValidateShare)
			}
		} else {
			logger.L.Warn("postgres unavailable, /files /trash /shares /s routes disabled")
		}

		// 管理端模块：受 JWT + AdminOnly 双中间件保护
		if adb != nil && la != nil {
			ah := handler.NewAdminHandler(
				adminstore.NewUserRepo(adb.GORM),
				adminstore.NewFileRepo(adb.GORM),
				adminstore.NewLogQueryRepo(adb.GORM),
				la,
				notifier,
			)
			adminGrp := v1.Group("/admin", middleware.GinJWTAuth(jwtMgr), middleware.GinAdminOnly())
			{
				adminGrp.GET("/users", ah.ListUsers)
				adminGrp.PATCH("/users/:id/status", ah.UpdateUserStatus)
				adminGrp.PATCH("/users/:id/quota", ah.UpdateUserQuota)
				adminGrp.POST("/users/quota/reset", ah.ResetAllQuota)
				adminGrp.GET("/files", ah.ListFiles)
				adminGrp.GET("/logs", ah.ListLogs)
			}
		} else {
			logger.L.Warn("adminstore unavailable, /admin routes disabled")
		}

		// 配额实时推送：SSE 长连接，JWT 保护。Redis/PG 不可用时 handler 未构造，跳过。
		if sseHandler != nil {
			v1.GET("/quota/stream", middleware.GinJWTAuthAllowQuery(jwtMgr), sseHandler.Stream)
		} else {
			logger.L.Warn("quota SSE endpoint disabled: redis or postgres unavailable")
		}
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
