# 本地 Docker 与 Prometheus 可观测性设计

## 目标

为 `user-order-api` 建立可在本地完整验收的容器化运行和指标采集基础：一条命令启动 API、MySQL 与 Prometheus；通过健康端点确认服务状态；通过 Prometheus 查看真实请求、数据库连接池和审计队列指标。

## 范围与边界

本批次包含：

- API 的多阶段 Docker 镜像；
- Docker Compose 中的 `api`、`mysql`、`prometheus` 三个服务；
- 存活检查 `/healthz`、就绪检查 `/readyz` 与 Prometheus 抓取端点 `/metrics`；
- HTTP、MySQL 连接池和审计队列的 Prometheus 指标；
- 本地运行、验证与排障文档。

本批次不包含 Grafana、Alertmanager、云厂商账号或托管监控、OpenTelemetry 链路追踪、日志集中化、分布式限流或 Redis。它们需要在明确部署平台、流量与告警责任后作为后续小批次引入。

## 运行拓扑

```text
浏览器 / Postman ── localhost:8888 ──> API 容器
                                          ├─ mysql:3306 ──> MySQL 容器 / 持久化数据卷
                                          └─ /metrics <──── Prometheus 容器

浏览器 ── localhost:9090 ──> Prometheus 查询界面
```

Compose 创建默认内部网络，API 在该内部网络中以 `mysql:3306` 连接数据库。为保持 Navicat 本地查看数据的现有开发方式，开发 Compose 继续映射 MySQL 的 `3307:3306`；生产部署配置不得暴露 MySQL 端口。API 映射 `8888`，Prometheus 映射 `9090`。

## Docker 镜像与配置

新增多阶段 `Dockerfile`：构建阶段使用与项目 Go 版本一致的官方 Go 镜像编译 `./cmd/api`；运行阶段只包含 API 二进制和 TLS 根证书，以非 root 用户运行。镜像不包含数据库密码、JWT 密钥或本地数据。

Compose 的 `api` 服务从该 Dockerfile 构建，使用环境变量注入 `MYSQL_DSN`、`JWT_SIGNING_KEY`、Cookie 安全配置和监听端口。`mysql` 继续使用命名卷保存数据，并以健康检查作为 API 的启动依赖。生产密钥不写入版本库；本地 Compose 只提供开发用默认值，并在文档中标明不得复用到生产。

## 健康端点

保留已有 `/api/v1/health` 兼容现有调用，新增无需认证的根路径端点：

| 端点 | 语义 | 成功 | 失败 |
| --- | --- | --- | --- |
| `/healthz` | 存活检查：HTTP 进程和路由能响应。不会访问数据库。 | `200 {"status":"ok"}` | 仅在服务无法处理请求时失败。 |
| `/readyz` | 就绪检查：服务可在短超时内连接 MySQL。内存测试组装视为就绪。 | `200 {"status":"ok"}` | `503 {"status":"not_ready"}`。 |
| `/metrics` | Prometheus 文本格式指标，仅供监控采集。 | `200 text/plain; version=0.0.4` | 无数据库依赖。 |

容器健康检查请求 `/healthz`。负载均衡或部署平台应使用 `/readyz` 判断是否向实例发送业务流量。

## 指标设计

使用 `github.com/prometheus/client_golang/prometheus` 与 `promhttp`，由应用组合根创建并注入单一注册表。`/metrics` 输出 Go/runtime 采集器、进程采集器和以下应用指标：

| 指标 | 类型 | 标签 / 含义 |
| --- | --- | --- |
| `user_order_api_http_requests_total` | Counter | `method`、规范化 `route`、`status`；累计 HTTP 请求数。 |
| `user_order_api_http_request_duration_seconds` | Histogram | `method`、规范化 `route`、`status`；请求耗时。 |
| `user_order_api_http_requests_in_flight` | Gauge | 当前处理中的请求数。 |
| `user_order_api_mysql_open_connections` | Gauge | `database/sql` 当前打开连接数。 |
| `user_order_api_mysql_in_use_connections` | Gauge | 当前被占用连接数。 |
| `user_order_api_mysql_idle_connections` | Gauge | 当前空闲连接数。 |
| `user_order_api_audit_queue_pending` | Gauge | 审计异步队列当前待处理数量。 |
| `user_order_api_audit_dropped_total` | Counter | 队列满时被丢弃的尽力而为审计记录数。 |

`route` 必须使用有限、固定的模板值，如 `/api/v1/orders/:id`，绝不使用真实订单 ID、邮箱、用户 ID、请求 ID 或错误文本，避免 Prometheus 时序基数无限增长。指标中不记录密码、JWT、Refresh Token 或个人数据。

数据库连接池和审计队列指标通过自定义 Collector 在 Prometheus 抓取时读取当前状态，不在请求路径中额外轮询数据库。

## Prometheus 采集

新增 Prometheus 配置，将抓取目标设为 Compose 内部网络的 `api:8888`，抓取路径 `/metrics`，间隔 15 秒。Prometheus 的数据使用命名卷保存。启动后从宿主机访问 `http://localhost:9090`，可查询：

```promql
sum(rate(user_order_api_http_requests_total[5m])) by (route, status)
histogram_quantile(0.95, sum(rate(user_order_api_http_request_duration_seconds_bucket[5m])) by (le, route))
user_order_api_mysql_in_use_connections
user_order_api_audit_queue_pending
```

## 错误处理与安全

- API 启动仍在 MySQL 不可用或迁移失败时失败，不回退至内存存储。
- `/readyz` 仅反映依赖就绪，失败时返回 `503`，不暴露 DSN、MySQL 错误文本或密钥。
- `/metrics` 公开的本地端口仅用于开发验收；生产环境应限制为 Prometheus 所在私有网络或由反向代理/IP 白名单保护。
- 容器运行时使用 `AUTH_COOKIE_SECURE=false` 仅适用于 `http://localhost`；HTTPS 生产环境必须启用该值。

## 验收标准

1. `docker compose up --build` 可启动 API、MySQL 和 Prometheus，且 MySQL 数据保存在命名卷中。
2. `GET /healthz` 返回 200；MySQL 不可用时 `GET /readyz` 返回 503，恢复后返回 200。
3. 访问业务 API 后，Prometheus 在 15 秒内抓取到 HTTP 计数与耗时指标；指标路由不含动态 ID。
4. Prometheus 页面可查询 API、数据库连接池和审计队列指标。
5. 单元测试覆盖端点语义、路由规范化、指标记录和数据库/审计 Collector；现有 `go test ./...`、`go vet ./...`、`go test -race ./...` 通过。
