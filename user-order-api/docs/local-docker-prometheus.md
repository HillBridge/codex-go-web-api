# 本地 Docker 与 Prometheus 可观测性

本文说明如何在本地以 Docker 运行 `user-order-api`、MySQL 和 Prometheus，并查看 API 的真实运行指标。

## 目标

- 使用一条命令启动 API、MySQL 与 Prometheus；
- 通过健康端点区分“进程活着”和“数据库已就绪”；
- 使用 Prometheus 查看 HTTP、MySQL 连接池与审计队列指标；
- 使用 Jaeger 查看请求 Trace，并使用 Alertmanager 查看本地告警；
- 保留 Navicat 通过 `127.0.0.1:3307` 查看本地 MySQL 的使用方式。

## 运行结构

```text
浏览器 / Postman ── localhost:8888 ──> API 容器
                                          ├─ mysql:3306 ──> MySQL 容器 / 数据卷
                                          ├─ /metrics <──── Prometheus 容器 ──> Alertmanager
                                          └─ OTLP/gRPC ──> Jaeger 容器

浏览器 ── localhost:9090 ──> Prometheus 查询界面
浏览器 ── localhost:16686 ──> Jaeger Trace 界面
浏览器 ── localhost:9093 ──> Alertmanager 告警界面
```

Compose 会创建内部网络。API 在容器网络中通过 `mysql:3306` 连接 MySQL；开发环境仍映射 `3307:3306`，因此 Navicat 可连接 `127.0.0.1:3307`。生产环境不应暴露 MySQL 端口。

## 启动与停止

```bash
docker compose up --build -d
docker compose ps
```

服务地址：

| 服务 | 地址 | 用途 |
| --- | --- | --- |
| API | `http://localhost:8888` | 业务接口与健康检查 |
| MySQL | `127.0.0.1:3307` | Navicat 本地查看数据 |
| Prometheus | `http://localhost:9090` | 查询监控指标 |
| Jaeger | `http://localhost:16686` | 查询 HTTP/MySQL Trace |
| Alertmanager | `http://localhost:9093` | 查看告警状态、分组和静默 |

查看 API 日志或停止容器：

```bash
docker compose logs -f api
docker compose down
```

`docker compose down` 会保留 MySQL 和 Prometheus 数据卷。不要在日常开发中执行 `docker compose down -v`，因为它会删除这些本地数据卷。

## 健康检查

| 端点 | 含义 | 正常响应 |
| --- | --- | --- |
| `/healthz` | 存活检查，只确认 HTTP 进程能响应。 | `200 {"status":"ok"}` |
| `/readyz` | 就绪检查，额外确认 MySQL 可连接。 | `200 {"status":"ok"}` |
| `/metrics` | Prometheus 文本格式指标。 | `200 text/plain` |

```bash
curl http://localhost:8888/healthz
curl http://localhost:8888/readyz
curl http://localhost:8888/metrics | rg 'user_order_api_http_requests_total'
```

MySQL 不可用时，`/readyz` 返回 `503 {"status":"not_ready"}`，且不会返回数据库连接串或错误细节。

## 指标

Prometheus 每 15 秒读取一次 API 的 `/metrics`。指标不会包含密码、JWT、Refresh Token、邮箱、用户 ID、订单 ID、请求 ID 或错误文本。

| 指标 | 含义 |
| --- | --- |
| `user_order_api_http_requests_total` | 按方法、固定路由模板和状态码统计的请求总数。 |
| `user_order_api_http_request_duration_seconds` | 请求耗时直方图，可计算 P95 等延迟。 |
| `user_order_api_http_requests_in_flight` | 当前正在处理的 HTTP 请求数。 |
| `user_order_api_mysql_open_connections` | MySQL 当前打开连接数。 |
| `user_order_api_mysql_in_use_connections` | MySQL 当前正在使用的连接数。 |
| `user_order_api_mysql_idle_connections` | MySQL 当前空闲连接数。 |
| `user_order_api_mysql_max_open_connections` | MySQL 连接池配置的最大连接数。 |
| `user_order_api_audit_queue_pending` | 等待异步处理的审计记录数。 |
| `user_order_api_audit_dropped_total` | 审计队列满时丢弃的尽力而为记录数。 |

路由标签只使用固定模板，例如 `/api/v1/orders/:id`，不会将真实订单 ID 写入指标，避免指标数据无限膨胀。

## Prometheus 查询示例

打开 `http://localhost:9090`，在查询框中输入：

```promql
sum(rate(user_order_api_http_requests_total[5m])) by (route, status)
```

查看各路由近 5 分钟的请求速率和状态码分布。

```promql
histogram_quantile(0.95, sum(rate(user_order_api_http_request_duration_seconds_bucket[5m])) by (le, route))
```

查看各路由 P95 响应耗时。

```promql
user_order_api_mysql_in_use_connections
```

查看当前被占用的 MySQL 连接数。

```promql
user_order_api_audit_queue_pending
```

查看审计队列是否存在积压。

## 链路追踪

Compose 会将 API 的 Trace 通过 OTLP/gRPC 发送到 Jaeger。打开 `http://localhost:16686`，在 Service 下拉框选择 `user-order-api`，即可查看请求的 HTTP span；访问注册、登录或订单接口时，还能看到对应的 MySQL 子 span。HTTP span 和指标都使用固定路由模板，不会把真实用户 ID 或订单 ID 写入名称。

请求日志包含 `request_id`、`trace_id` 和 `span_id`。可用 Jaeger 中的 Trace ID 与容器日志关联排查一次请求；日志不会记录 Authorization、Cookie、Refresh Token、密码或 DSN。

API 直接在宿主机运行时可以不设置 `OTEL_EXPORTER_OTLP_ENDPOINT`，此时 Trace 使用 no-op Provider，不要求本机安装 Jaeger。Compose 默认使用以下配置：

```text
OTEL_SERVICE_NAME=user-order-api
OTEL_EXPORTER_OTLP_ENDPOINT=jaeger:4317
OTEL_EXPORTER_OTLP_INSECURE=true
```

## 告警与故障演练

Prometheus 会加载 `docker/prometheus/alerts.yml` 中的规则，并将告警发送给本地 Alertmanager。当前规则包括 API 不可用、5xx 响应、审计事件丢弃和 MySQL 连接池使用率超过 80%。打开 Prometheus 的 Alerts 页面或 `http://localhost:9093` 查看状态；本地接收器只做展示、分组、去重和静默，不会向外部发送邮件或消息。

可安全执行一次 API 故障演练（不会删除任何数据库数据）：

```bash
docker compose up --build -d
curl --fail http://localhost:8888/api/v1/health
docker compose stop api
# 等待至少 45 秒，在 Prometheus Alerts 和 Alertmanager 查看 UserOrderAPIUnavailable
docker compose start api
```

API 恢复后等待下一次抓取，告警会变为 resolved。演练只允许停止和启动 `api`；不要执行 `docker compose down -v`，因为该命令会删除本地 MySQL、Prometheus、Jaeger 和 Alertmanager 数据卷。

## 安全边界

- 本地 Compose 中的 `JWT_SIGNING_KEY` 仅供开发使用，不能复用于生产。
- 本地 HTTP 环境使用 `AUTH_COOKIE_SECURE=false`；生产 HTTPS 环境必须启用 `AUTH_COOKIE_SECURE=true`。
- `/metrics` 在生产环境应只向 Prometheus 所在私有网络开放，或由反向代理/IP 白名单保护。
- Docker 镜像不包含数据库密码、JWT 密钥或本地数据库数据；这些内容通过运行环境变量和数据卷提供。
