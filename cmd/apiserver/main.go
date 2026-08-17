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
	"github.com/Demetrius2107/NimbusDrive/internal/handler"
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

	jwtMgr := auth.New(cfg.JWT.Secret, cfg.JWT.AccessExpMin, cfg.JWT.RefreshExpDay, cfg.JWT.Issuer)

	// 契约注册表：启动期编译所有 JSON Schema，失败即 fatal。
	reg, err := contract.NewRegistry()
	if err != nil {
		logger.L.Fatal("contract registry init failed", zap.Error(err))
	}
	registerRoutes(r, st, rc, adb, jwtMgr, reg)

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

func registerRoutes(r *gin.Engine, st *store.Store, rc *cache.Redis, adb *adminstore.DB, jwtMgr *auth.JWTManager, reg *contract.Registry) {
	r.GET("/healthz", healthz(st, rc))

	v1 := r.Group("/api/v1")
	{
		// 鉴权模块：register/login 公开，me 需鉴权
		if st != nil {
			authHandler := handler.NewAuthHandler(st.Repos().Users, jwtMgr)
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
			sh := handler.NewShareHandler(st.Repos().Shares, st.Repos().Files, rc, st.DB)
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
		if adb != nil {
			if err := adb.Migrate(context.Background()); err != nil {
				logger.L.Warn("admin migrate failed", zap.Error(err))
			}
			la := adminstore.NewLogAggregator(adb.GORM, 1024, 50, 5*time.Second)
			defer la.Close()

			ah := handler.NewAdminHandler(
				adminstore.NewUserRepo(adb.GORM),
				adminstore.NewFileRepo(adb.GORM),
				adminstore.NewLogQueryRepo(adb.GORM),
				la,
			)
			adminGrp := v1.Group("/admin", middleware.GinJWTAuth(jwtMgr), middleware.GinAdminOnly())
			{
				adminGrp.GET("/users", ah.ListUsers)
				adminGrp.PATCH("/users/:id/status", ah.UpdateUserStatus)
				adminGrp.PATCH("/users/:id/quota", ah.UpdateUserQuota)
				adminGrp.GET("/files", ah.ListFiles)
				adminGrp.GET("/logs", ah.ListLogs)
			}
		} else {
			logger.L.Warn("adminstore unavailable, /admin routes disabled")
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
