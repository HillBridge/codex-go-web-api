# 本地 Docker 与 Prometheus 可观测性

本文说明如何在本地以 Docker 运行 `user-order-api`、MySQL 和 Prometheus，并查看 API 的真实运行指标。

## 目标

- 使用一条命令启动 API、MySQL 与 Prometheus；
- 通过健康端点区分“进程活着”和“数据库已就绪”；
- 使用 Prometheus 查看 HTTP、MySQL 连接池与审计队列指标；
- 保留 Navicat 通过 `127.0.0.1:3307` 查看本地 MySQL 的使用方式。

## 运行结构

```text
浏览器 / Postman ── localhost:8888 ──> API 容器
                                          ├─ mysql:3306 ──> MySQL 容器 / 数据卷
                                          └─ /metrics <──── Prometheus 容器

浏览器 ── localhost:9090 ──> Prometheus 查询界面
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

## 安全边界

- 本地 Compose 中的 `JWT_SIGNING_KEY` 仅供开发使用，不能复用于生产。
- 本地 HTTP 环境使用 `AUTH_COOKIE_SECURE=false`；生产 HTTPS 环境必须启用 `AUTH_COOKIE_SECURE=true`。
- `/metrics` 在生产环境应只向 Prometheus 所在私有网络开放，或由反向代理/IP 白名单保护。
- Docker 镜像不包含数据库密码、JWT 密钥或本地数据库数据；这些内容通过运行环境变量和数据卷提供。
