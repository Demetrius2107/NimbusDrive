# NimbusDrive

> 轻量、开放协议的个人/团队云存储系统。Go 双服务后端 + React 三端前端。

状态：**P1 MVP 骨架已搭建**（2026-08-16）。设计文档完备，代码骨架可编译可运行，业务逻辑待实现。

## 技术栈

| 层 | 选型 |
|----|------|
| 后端 | Go + **Gin**(APIServer) + **Hertz**(TransferServer) + sqlx/pgx + GORM |
| 存储 | PostgreSQL 16 + Redis 7 + MinIO（S3 兼容） |
| Web 客户端 | React 18 + TS + Vite + Ant Design 5 + React Router v7 + Zustand + TanStack Query |
| 管理后台 | React 18 + TS + Vite + Ant Design 5（与客户端同栈） |
| 移动端 | React Native (Expo) |
| 部署 | Docker Compose（MVP）→ K8s（后期） |
| 协议 | Apache 2.0 |

## 仓库结构

```
NimbusDrive/
├── cmd/
│   ├── apiserver/          # Gin 服务入口（用户/鉴权/管理端/配额/文件元数据/分享）
│   └── transferserver/     # Hertz 服务入口（上传/下载/分块传输）
├── internal/
│   ├── domain/             # 聚合根、枚举、错误码（两服务共享）
│   ├── store/              # sqlx + pgx 数据访问层（核心链路）
│   ├── adminstore/         # GORM（管理后台 CRUD 专用）
│   ├── storage/            # MinIO 客户端封装
│   ├── cache/              # go-redis 封装
│   ├── config/             # viper 配置加载
│   ├── logger/             # zap 封装
│   └── middleware/         # Gin/Hertz 各自中间件薄层
├── pkg/response/           # 统一响应体
├── configs/                # 配置文件（config.dev.yaml）
├── migrations/             # SQL 迁移（0001_init.sql）
├── docs/                   # 设计文档体系（见下）
├── web/                    # Web 客户端
├── admin/                  # 管理后台
└── mobile/                 # React Native (Expo) 移动端
```

## 文档

`docs/` 下维护完整设计基线，动代码前的决策均以文档为准：

- `立项决策与规划_v1.md` — 定位、竞品、技术栈、路线图
- `实现方案_v1.md` — 架构、模块、状态机、边界条件、协议矩阵、OpenAPI
- `设计文档_v1.md` — 领域模型、双服务边界、DDL、缓存/对象存储、时序图、并发策略
- `接口文档_v1.md` — OpenAPI 规范
- `功能文档_v1.md` / `事件风暴_v1.md` / `业务场景判断_v1.md` — 五大核心场景

## 快速开始

### 前置依赖

- **Go 1.25+（amd64）** — 见下方"关于 Go 架构"说明
- **Node.js 20+** + npm
- PostgreSQL 16、Redis 7、MinIO（本地或 Docker）

### 后端

```bash
# 1. 准备数据库（执行 DDL）
psql -U nimbus -d nimbusdrive -f migrations/0001_init.sql

# 2. 按需修改 configs/config.dev.yaml（PG/Redis/MinIO 连接信息）

# 3. 构建（amd64 目标）
GOARCH=amd64 go build -o bin/apiserver ./cmd/apiserver
GOARCH=amd64 go build -o bin/transferserver ./cmd/transferserver

# 4. 启动
./bin/apiserver          # :8080
./bin/transferserver     # :8081

# 5. 健康检查
curl http://localhost:8080/healthz
curl http://localhost:8081/healthz
```

> 骨架阶段即使 PG/Redis/MinIO 未就绪，服务仍会启动（降级模式，`/healthz` 显示对应 `skip`/`fail`）。

### 前端（Web 客户端）

```bash
cd web
npm install
npm run dev      # http://localhost:5173，/api 代理到 :8080
```

### 管理后台

```bash
cd admin
npm install
npm run dev      # http://localhost:5174
```

### 移动端

```bash
cd mobile
npm install
npx expo start   # 按 i 启动 iOS 模拟器 / a 启动 Android / w 启动 Web
```

## 关于 Go 架构（重要）

本机默认 `go`（`/d/go1.25.4`，`windows/386`）为 **32 位** 工具链。Hertz 的传递依赖 `ameda` 在 32 位下会因 `math.MaxInt64` 溢出而编译失败，且 `bytedance/sonic` 在某些 Go 版本下有链接器问题。

**解决方案**：目标平台是 amd64，构建时显式指定 `GOARCH=amd64`：

```bash
GOARCH=amd64 go build ./...
```

或安装 64 位 Go（如 `/d/go` 的 Go 1.26.6 amd64）并将其设为默认。

## 服务边界（双服务拆分）

| 服务 | 框架 | 端口 | 职责 |
|------|------|------|------|
| APIServer | Gin | 8080 | 用户/鉴权、管理端、配额、文件元数据 CRUD、分享、文件列表、回收站 |
| TransferServer | Hertz | 8081 | 上传分块接收与合并、下载 Range 直传、上传会话状态机 |

两服务共享 `internal/` 公共包（domain/store/storage/cache/config）。跨服务交互：MVP 阶段 TransferServer 完成上传后直接通过共享 store 写回元数据；后期演进为 asynq 事件驱动。详见 `docs/设计文档_v1.md` 第三章。

## 路线图

| 阶段 | 目标 |
|------|------|
| P0 立项 | ✅ 定位/命名/技术栈定稿 |
| P1 MVP | ⏳ Web 端上传/下载（分块、断点、秒传）/分享/用户/配额；Docker 部署 |
| P2 协议 | WebDAV + S3 + HTTP 直链 |
| P3 传输扩展 | 离线下载队列（HTTP/FTP）→ 磁力 |
| P4 协作 | 团队空间/权限/共享 |
| P5 成本 | 冷热分层/CDN/可选 P2P |

## 许可证

Apache License 2.0
