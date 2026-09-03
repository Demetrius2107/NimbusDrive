# NimbusDrive 前端镜像：Vite 构建 → nginx 托管 + API 反向代理。
#
# 路由规则与开发期 vite proxy 一致（见 web/vite.config.ts）：
#   /api/v1/upload、/api/v1/download → TransferServer :8081（分块/流式传输）
#   /api/*                            → APIServer :8080（其余控制面接口）
#   其余路径                           → SPA 静态资源（history 路由 fallback index.html）
#
# SSE（/api/v1/quota/stream）单独 location：关闭 proxy_buffering，
# 读超时拉长到 1h（心跳 30s 不会触发超时，仅防呆）。
# 上传分块 4MB（web/src/lib/uploader.ts CHUNK_SIZE），body 上限留 8x 余量。

# REGISTRY 基础镜像源前缀，默认 docker.io；网络受限环境可在构建时切换
# 镜像代理（如 --build-arg REGISTRY=docker.m.daocloud.io）。
ARG REGISTRY=docker.io
# NPM_REGISTRY 依赖源，网络受限环境切镜像（如 https://registry.npmmirror.com）
ARG NPM_REGISTRY=https://registry.npmjs.org

FROM ${REGISTRY}/node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --registry=${NPM_REGISTRY}
COPY . .
RUN npm run build

FROM ${REGISTRY}/nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
