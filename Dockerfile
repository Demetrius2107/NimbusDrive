# NimbusDrive Go 服务镜像（APIServer / TransferServer 共用一个构建模板）。
#
# 用 SERVICE 构建参数选择二进制：
#   docker build --build-arg SERVICE=apiserver -t nimbus-api .
#   docker build --build-arg SERVICE=transferserver -t nimbus-transfer .
# 构建上下文必须是仓库根目录（compose 已按此配置）。
#
# 运行层 alpine + ca-certificates：出站 TLS（MinIO/OSS）与时区所需；
# 配置文件从镜像内置 configs/config.prod.yaml 读，敏感字段用 NIMBUS_* 环境变量覆盖。
# 日志仅 stdout（config.prod log_dir 为空），交给 docker logs / Loki 采集。

# syntax=docker/dockerfile:1
# REGISTRY 基础镜像源前缀，默认 docker.io；网络受限环境可在构建时切换
# 镜像代理（如 docker build --build-arg REGISTRY=docker.m.daocloud.io）。
ARG REGISTRY=docker.io
ARG GO_VERSION=1.25
# GOPROXY 模块代理，网络受限环境切镜像（如 https://goproxy.cn,direct）
ARG GOPROXY=https://proxy.golang.org,direct

FROM ${REGISTRY}/golang:${GO_VERSION}-alpine AS build
ARG SERVICE=apiserver
ARG GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go env -w GOPROXY=${GOPROXY} && go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

FROM ${REGISTRY}/alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 nimbus
COPY --from=build /out/app /usr/local/bin/nimbus
# config.Load 搜索路径是 ./configs 与 ./（相对工作目录 /），故放到 /configs/
COPY configs/config.prod.yaml /configs/config.prod.yaml
WORKDIR /
USER nimbus
ENV NIMBUS_CONFIG_NAME=config.prod
EXPOSE 8080 8081
ENTRYPOINT ["nimbus"]
