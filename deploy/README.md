# NimbusDrive 部署

## 全栈一键部署（推荐）

前置：Docker + Docker Compose v2（含 `include` 支持）。

```bash
cd deploy
docker compose up -d                          # 中间件 + 双后端 + 前端
# 可选：追加可观测栈（Prometheus/Loki/Tempo/Grafana）
docker compose --profile observability up -d
```

启动后：

| 入口 | 地址 |
|------|------|
| Web 前端 | http://localhost |
| APIServer 直连 | http://localhost:8080（/healthz、/metrics） |
| TransferServer 直连 | http://localhost:8081（/healthz、/metrics） |
| MinIO 控制台 | http://localhost:9001（minioadmin/minioadmin） |
| Grafana（profile 启用时） | http://localhost:3000（admin/admin） |

验证：

```bash
docker compose ps          # 全部 healthy / running
curl http://localhost/healthz 2>/dev/null || curl http://localhost:8080/healthz
docker compose logs -f apiserver
```

## 网络受限环境（国内）

构建需要四类外网资源，全部可经 `deploy/.env` 切镜像代理：

| 变量 | 用途 | 缺省 |
|------|------|------|
| `DOCKER_REGISTRY` | Go/前端镜像的基础镜像源 | `docker.io` |
| `GOPROXY` | Go 模块下载代理 | `https://proxy.golang.org,direct` |
| `NPM_REGISTRY` | npm 依赖源 | `https://registry.npmjs.org` |

示例（`deploy/.env`）：

```
DOCKER_REGISTRY=docker.m.daocloud.io
GOPROXY=https://goproxy.cn,direct
NPM_REGISTRY=https://registry.npmmirror.com
```

**注意**：前端 `web/package-lock.json` 的 `resolved` 字段固定了
`mirrors.cloud.tencent.com` 下载地址，`npm ci` 优先按 lockfile 拉包、
`--registry` 对已锁定包无效。若该镜像源在你当前网络不可达，报
`npm error network`，可先在本机 `cd web && npm install --registry=https://registry.npmmirror.com`
重新解析生成 lockfile，或换可达网络构建。

## 凭据与配置

- 复制 `.env.example` 为 `deploy/.env` 修改；不改则用本地部署默认值。
  生产必须覆盖：`POSTGRES_PASSWORD`、`MINIO_ROOT_USER/PASSWORD`、`JWT_SECRET`。
- 应用配置基线是仓库根 `configs/config.prod.yaml`（容器拓扑：服务间走容器 DNS、
  日志走 stdout、OTLP 推 Tempo）。个别字段可用 `NIMBUS_*` 环境变量覆盖
  （viper 规则：`NIMBUS_POSTGRES_HOST` → `postgres.host`）。
- 预签名下载：浏览器直连 MinIO 拉文件，端点由 `MINIO_PUBLIC_ENDPOINT` 控制，
  默认 `localhost:9000`（单机）。域名/HTTPS 部署时改成对外域名。

## 拓扑与文件对照

| 内容 | 文件 |
|------|------|
| 全栈编排（含可观测 profile） | `deploy/docker-compose.yml` |
| 基础设施栈（PG/Redis/MinIO + 迁移 init） | `deploy/infra/docker-compose.yml`（被全栈 include） |
| Go 服务镜像（SERVICE 构建参数二选一） | `Dockerfile` |
| 前端镜像（nginx 反代 + SPA） | `web/Dockerfile` + `web/nginx.conf` |
| 容器拓扑应用配置 | `configs/config.prod.yaml` |
| Prometheus 容器网抓取变体 | `deploy/observability/prometheus.docker.yml` |

nginx 反代规则与开发期 vite proxy 一致：
`/api/v1/upload`、`/api/v1/download` → TransferServer；`/api/*`（含 SSE
`/api/v1/quota/stream`，已关缓冲）→ APIServer；其余 → SPA fallback。

## 注意事项

- **容器名冲突**：全栈沿用 infra 栈的固定容器名（nimbus-postgres 等）。
  启动全栈前先停掉开发栈：`cd deploy/infra && docker compose down`。
- **数据卷**：全栈使用 `nimbus_*` 前缀的新卷（project name 区分），与旧开发栈的
  `infra_*` 卷互不影响；旧数据要迁移时手动挂载复制。
- **首次启动**：`postgres-init` 容器按文件名顺序执行 `migrations/*.sql`（幂等）；
  MinIO bucket 由应用启动时自动创建。APIServer 是唯一跑 cron 的实例（控制面）。
- **本地开发不受影响**：仍可只起 infra 栈 + Go 进程跑宿主机
  （`cd deploy/infra && docker compose up -d` + `go run ./cmd/apiserver`）。

## 集成测试（依托容器环境）

`//go:build integration` 标签的集成测试连真实 PG/MinIO：

```bash
cd deploy/infra && docker compose up -d     # 或全栈
cd ../..
NIMBUS_TEST_DSN="host=127.0.0.1 port=5432 user=nimbus password=nimbus dbname=nimbusdrive sslmode=disable" \
NIMBUS_TEST_MINIO_ENDPOINT=127.0.0.1:9000 \
NIMBUS_TEST_MINIO_ACCESS_KEY=minioadmin \
NIMBUS_TEST_MINIO_SECRET_KEY=minioadmin \
go test -tags integration ./internal/... 
```
