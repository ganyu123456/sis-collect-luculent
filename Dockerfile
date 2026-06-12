# ===== 构建阶段 =====
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 安装 git（go mod 下载可能需要）
RUN apk add --no-cache git ca-certificates tzdata

# 先复制 go.mod/go.sum，利用 Docker layer 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# 编译，注入版本信息
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags "-s -w -X github.com/sis-collect-luculent/internal/app.Version=${VERSION}" \
    -o sis-collect-luculent ./main.go


# ===== 运行阶段（最小镜像） =====
FROM alpine:3.19

# 安装 ca-certificates（HTTPS/TLS 需要）和时区数据
RUN apk add --no-cache ca-certificates tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

COPY --from=builder /build/sis-collect-luculent .

# 默认配置文件和点表（可通过 ConfigMap 挂载覆盖）
COPY config.yaml .
COPY points.csv .

# 日志目录
RUN mkdir -p /app/logs

# 暴露健康检查端口
EXPOSE 8080

# 非 root 用户运行
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser

ENTRYPOINT ["./sis-collect-luculent"]
CMD ["-config", "config.yaml"]
