# sis-collect-luculent

SIS 数采网关服务：周期性从 SIS（实时数据库）HTTP 接口拉取测点数据，经 MQTT 协议上报至工业云边协同平台，并支持平台侧动态下发采集指令。

## 功能特性

- **批量采集**：基于 CSV 点表，按配置的批次大小分批请求 SIS HTTP 接口
- **MQTT 上报**：采集结果按批发布到 `data` 主题，超量测点自动拆包
- **状态上报**：定期推送采集状态（运行状态、在线测点数、采集间隔等）到 `status` 主题
- **平台指令**：订阅 `cmd` 主题，支持动态修改采集间隔（`collectInterval`）和启停任务（`taskControl`）
- **断线重连**：MQTT 连接断开后按指数退避策略自动重连
- **健康检查**：内置 HTTP 服务，提供 `/health`（存活探针）和 `/ready`（就绪探针）接口
- **多架构镜像**：CI 自动构建 `linux/amd64` 和 `linux/arm64` 双架构镜像并推送到 Harbor
- **优雅退出**：监听 `SIGINT` / `SIGTERM`，退出前完成当前采集批次

## 架构概览

```
SIS HTTP API ──批量拉取──► Collector ──► App ──MQTT Publish──► 平台 (data / status)
                                          ▲
                                          └──MQTT Subscribe── 平台 (cmd)
```

## 目录结构

```
.
├── main.go                     # 程序入口，日志初始化与信号处理
├── config.yaml                 # 默认配置文件
├── points.csv                  # 测点点表（CSV 格式）
├── Dockerfile                  # 多阶段构建镜像
├── go.mod / go.sum
├── config/                     # 配置加载与结构体定义
├── internal/
│   ├── app/
│   │   ├── app.go              # 核心调度逻辑（采集循环、MQTT 发布/订阅）
│   │   └── health.go           # HTTP 健康检查服务
│   ├── collector/
│   │   └── sis.go              # SIS HTTP 采集器
│   ├── mqtt/
│   │   └── client.go           # MQTT 客户端（断线重连、TLS）
│   └── pointtable/             # CSV 点表加载与管理
└── .github/workflows/
    └── build-push.yml          # CI：构建多架构镜像 + 创建 GitHub Release
```

## 快速开始

### 前置条件

- Go 1.23+
- 可访问的 SIS HTTP 服务
- MQTT Broker（如 EMQX、Mosquitto）

### 本地运行

```bash
# 克隆仓库
git clone https://github.com/<your-org>/sis-collect-luculent.git
cd sis-collect-luculent

# 安装依赖
go mod download

# 修改配置
cp config.yaml config.local.yaml
# 编辑 config.local.yaml，填写 SIS 地址和 MQTT Broker

# 编辑点表
# points.csv 格式：name,enabled（第一行为表头）

# 运行
go run ./main.go -config config.local.yaml
```

### Docker 运行

```bash
docker run -d \
  --name sis-collect-luculent \
  -v $(pwd)/config.yaml:/app/config.yaml \
  -v $(pwd)/points.csv:/app/points.csv \
  -p 8080:8080 \
  harbor.zkjgy.online/library/sis-collect-luculent:latest
```

## 配置说明

```yaml
# SIS HTTP 接口配置
sis:
  base_url: "http://localhost:7757"       # SIS 服务地址，支持 ${SIS_BASE_URL} 环境变量
  data_point_path: "/api/Values/GetDataPoint"
  timeout_seconds: 10                     # 单次请求超时（秒）
  batch_size: 50                          # 每批请求的测点数量

# MQTT 连接配置
mqtt:
  broker: "tcp://localhost:1883"          # Broker 地址，支持 ${MQTT_BROKER} 环境变量
  client_id: "sis-collect-luculent-001"
  username: ""                            # 支持 ${MQTT_USERNAME} 环境变量
  password: ""                            # 支持 ${MQTT_PASSWORD} 环境变量
  keep_alive: 60
  clean_session: true
  qos: 1
  tls_enable: false                       # 启用 TLS 时填写以下证书路径
  tls_ca_cert: ""
  tls_client_cert: ""
  tls_client_key: ""
  reconnect_min_seconds: 1               # 断线重连最小等待时间
  reconnect_max_seconds: 120             # 断线重连最大等待时间

# 采集任务配置
collect:
  topic:
    device_id: "sis-001"                 # 设备唯一编码
    status_topic: "device/sis-001/status"
    cmd_topic:    "device/sis-001/cmd"
    data_topic:   "device/sis-001/data"
  collect_interval_s: 10                 # 采集间隔（秒），可被平台指令动态修改
  status_interval_s: 30                  # 状态上报间隔（秒）
  mqtt_batch_size: 100                   # 每条 MQTT 消息携带的测点数量

# 点表文件
point_csv: "points.csv"

# 健康检查
health:
  port: 8080

# 日志
log:
  level: "info"                          # debug / info / warn / error
  file_path: ""                          # 留空则仅输出控制台
```

## MQTT 消息格式

### data 主题（`device/{id}/data`）

```json
{
  "timestamp": 1718160000000,
  "gatewayId": "sis-001",
  "batchData": {
    "Tag.Point1": 23.5,
    "Tag.Point2": 100.0
  }
}
```

### status 主题（`device/{id}/status`）

```json
{
  "timestamp": 1718160000000,
  "runState": "Running",
  "taskControl": 1,
  "collectPointTotal": 200,
  "collectPointOnline": 198,
  "lastCollectTime": 1718159990000,
  "collectInterval": 10
}
```

### cmd 主题（`device/{id}/cmd`，平台下发）

```json
{
  "requestId": "req-001",
  "timestamp": 1718160000000,
  "params": {
    "collectInterval": 30,
    "taskControl": 0
  }
}
```

| 参数 | 类型 | 说明 |
|---|---|---|
| `collectInterval` | int | 新的采集间隔（秒），最小值为 1 |
| `taskControl` | int | `1` = 启动采集，`0` = 停止采集 |

## 健康检查接口

| 接口 | 说明 | 成功状态码 | 失败状态码 |
|---|---|---|---|
| `GET /health` | 存活探针，返回详细运行状态 | `200 OK` | `503` (MQTT 断连) |
| `GET /ready` | 就绪探针，MQTT 连接正常才就绪 | `200 OK` | `503` |

`/health` 响应示例：

```json
{
  "status": "ok",
  "task_running": true,
  "mqtt_connected": true,
  "collect_interval_s": 10,
  "point_total": 300,
  "point_enabled": 250,
  "online_count": 248,
  "last_collect_time": "2024-06-12T10:00:00Z",
  "uptime": "2h3m15s",
  "version": "v1.0.0"
}
```

## 构建镜像

```bash
# 本地构建（amd64）
docker build --build-arg VERSION=v1.0.0 -t sis-collect-luculent:latest .
```

CI 流程（推送到 `main` 分支或打 tag 时自动触发）：

1. 并行构建 `linux/amd64` 和 `linux/arm64` 镜像并推送到 Harbor
2. 创建 multi-arch manifest（`latest` tag）
3. 打 tag 时额外打包镜像 tar.gz 并创建 GitHub Release

镜像地址：`harbor.zkjgy.online/library/sis-collect-luculent:latest`

## Kubernetes / KubeEdge 部署

ConfigMap 挂载配置文件和点表，覆盖镜像内置默认值：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sis-collect-luculent
spec:
  replicas: 1
  selector:
    matchLabels:
      app: sis-collect-luculent
  template:
    metadata:
      labels:
        app: sis-collect-luculent
    spec:
      containers:
        - name: sis-collect-luculent
          image: harbor.zkjgy.online/library/sis-collect-luculent:latest
          args: ["-config", "/app/config.yaml"]
          ports:
            - containerPort: 8080
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
          volumeMounts:
            - name: config
              mountPath: /app/config.yaml
              subPath: config.yaml
            - name: config
              mountPath: /app/points.csv
              subPath: points.csv
          env:
            - name: SIS_BASE_URL
              value: "http://sis-service:7757"
            - name: MQTT_BROKER
              value: "tcp://emqx:1883"
      volumes:
        - name: config
          configMap:
            name: sis-collect-luculent-config
```

## 开发说明

```bash
# 编译
go build -o sis-collect-luculent ./main.go

# 运行测试
go test ./...

# 代码检查
golangci-lint run
```

版本信息通过 ldflags 注入：

```bash
go build -ldflags "-X github.com/sis-collect-luculent/internal/app.Version=v1.0.0" -o sis-collect-luculent ./main.go
```

## 本地测试

项目内置了完整的本地测试环境，包含：

- `test/sis-simulator/main.go` — SIS HTTP API 模拟器，模拟真实测点数据（随机波动 + 概率离线）
- `test/mosquitto/mosquitto.conf` — Mosquitto 配置文件
- `test/config.test.yaml` — 测试专用配置（采集间隔 5s、batch_size 缩小便于观察）

### 前置条件

- Go 1.23+
- Docker（仅用于运行 MQTT Broker）

### 启动测试环境

**第一步：启动 MQTT Broker**

```bash
docker run -d --name test-mosquitto -p 1883:1883 \
  -v $(pwd)/test/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro \
  eclipse-mosquitto:2.0
```

**第二步：启动 SIS 模拟器**（新终端）

```bash
go run ./test/sis-simulator/main.go
```

启动成功后输出：

```
SIS 模拟器启动，监听 :7757
支持的测点数量: 10
接口: POST /api/Values/GetDataPoint
```

**第三步：启动主应用**（新终端）

```bash
go run ./main.go -config test/config.test.yaml
```

启动成功后输出：

```
SIS 数采应用启动  {"sis_base_url": "http://localhost:7757", "mqtt_broker": "tcp://localhost:1883"}
点表加载完成      {"total": 11, "enabled": 10}
MQTT 连接成功     {"broker": "tcp://localhost:1883"}
数采应用启动      {"collect_interval_s": 5, "mqtt_batch_size": 4}
```

### 功能验证

**观察 MQTT 实时消息**

```bash
docker exec test-mosquitto mosquitto_sub -h localhost -t "device/#" -v
```

输出示例（每 5 秒一轮）：

```
device/sis-test-001/data   {"timestamp":...,"gatewayId":"sis-test-001","batchData":{"DDM.SIS.0DCS_00BHT03GT001XQ001":121.33,...}}
device/sis-test-001/status {"timestamp":...,"runState":"Running","collectPointTotal":10,"collectPointOnline":9,...}
```

**验证健康检查接口**

```bash
# 存活探针
curl http://localhost:8080/health

# 就绪探针
curl http://localhost:8080/ready
```

`/health` 响应示例：

```json
{
  "status": "ok",
  "task_running": true,
  "mqtt_connected": true,
  "collect_interval_s": 5,
  "point_total": 11,
  "point_enabled": 10,
  "online_count": 9,
  "uptime": "20s",
  "version": "dev"
}
```

**下发平台指令（cmd 主题）**

```bash
# 修改采集间隔为 3 秒
docker exec test-mosquitto mosquitto_pub \
  -h localhost -t "device/sis-test-001/cmd" \
  -m '{"requestId":"req-001","timestamp":0,"params":{"collectInterval":3}}'

# 停止采集任务
docker exec test-mosquitto mosquitto_pub \
  -h localhost -t "device/sis-test-001/cmd" \
  -m '{"requestId":"req-002","timestamp":0,"params":{"taskControl":0}}'

# 重新启动采集
docker exec test-mosquitto mosquitto_pub \
  -h localhost -t "device/sis-test-001/cmd" \
  -m '{"requestId":"req-003","timestamp":0,"params":{"taskControl":1}}'
```

指令生效后可通过 `/health` 接口或 MQTT `status` 主题确认状态变化。

**直接调用 SIS 模拟器接口**

```bash
curl -s -X POST http://localhost:7757/api/Values/GetDataPoint \
  -H "Content-Type: application/json" \
  -d '{"Names":"DDM.SIS.0DCS_00BHT03GT001XQ001,DDM.SIS.0DCS_00BHT03GT001XQ002"}' \
  | python3 -m json.tool
```

### 停止测试环境

```bash
# 停止主应用和 SIS 模拟器：在各终端按 Ctrl+C

# 停止并删除 MQTT Broker 容器
docker stop test-mosquitto && docker rm test-mosquitto
```

## License

MIT
