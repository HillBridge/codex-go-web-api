# 阶段 7：本地链路追踪与告警设计

## 目标

在既有本地 Docker + Prometheus 栈上补齐两项可观测能力：

1. 使用 OpenTelemetry 与 Jaeger 查看单次请求的 HTTP 和 MySQL 调用链；
2. 使用 Prometheus 告警规则与 Alertmanager 查看本地触发的告警。

该设计仅面向本地 Compose 验收，不依赖云账号，不发送邮件、Slack 或其他外部通知。

## 范围与非范围

本次包含：

- Go 服务中的 OpenTelemetry Trace Provider、HTTP 入站 Trace、MySQL `database/sql` Trace 和结构化日志 `trace_id`；
- Compose 中的 Jaeger 与 Alertmanager；
- Prometheus 告警规则及 Alertmanager 本地接收器；
- MySQL 最大连接数指标，用于计算连接池使用率；
- 本地启动、查询、告警验证与故障排查文档。

本次不包含云托管 Trace、生产通知渠道、PagerDuty、邮件、Slack、自动扩缩容、集中日志平台、业务级 SLO 或跨服务调用。生产通知接收器应在确定组织的值班流程、密钥和权限后单独配置。

## 运行拓扑

```text
浏览器 / Postman
      │  traceparent（可选）
      ▼
API 容器 ── OTLP/gRPC ──> Jaeger 容器 ── localhost:16686（Trace 查询）
  │
  └─ /metrics ──────────> Prometheus 容器 ── 告警规则 ──> Alertmanager 容器
                                                                 │
                                                                 └─ localhost:9093（告警查看）
```

Prometheus 继续每 15 秒抓取 `api:8888/metrics`。Jaeger 的 OTLP 接收端口只在 Compose 内部网络使用；只有 Jaeger UI 的 `16686` 和 Alertmanager UI 的 `9093` 映射到宿主机。

## 链路追踪设计

### 服务配置

新增以下可选环境变量：

| 变量 | 本地 Compose 值 | 行为 |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `jaeger:4317` | OTLP gRPC Trace 接收端点；为空时不导出 Trace。 |
| `OTEL_SERVICE_NAME` | `user-order-api` | Jaeger 中显示的服务名。 |
| `OTEL_EXPORTER_OTLP_INSECURE` | `true` | Compose 内部明文 OTLP 连接；生产应使用受信 TLS。 |

应用启动时创建 OpenTelemetry Provider。未设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 时使用 no-op Provider，因此 `go run ./cmd/api` 不依赖 Jaeger；设置端点但 Provider 初始化失败时，应用启动失败，避免用户误以为已产生可用 Trace。

应用退出时在现有优雅关闭流程中执行 Trace Provider 的 `Shutdown`，给予 exporter 一个有限时间刷新已完成的 span。

### Span 边界与数据安全

- HTTP 入口使用 W3C Trace Context；客户端传入 `traceparent` 时继续该 Trace，否则创建新的 Trace。
- 每个 HTTP 请求产生一个服务器 span，名称为固定路由模板，例如 `GET /api/v1/orders/:id`，不使用真实 URL ID。
- `database/sql` 通过 OpenTelemetry SQL instrumentation 产生 MySQL 子 span，包含操作类型和耗时；不记录 DSN、密码、JWT、Cookie、Refresh Token、SQL 参数、邮箱或完整 SQL 文本。
- 现有请求日志新增 `trace_id` 与 `span_id`；请求 ID 仍保留。日志和 Trace 可以通过 `trace_id` 关联。
- `/metrics` 不导出敏感 Trace 数据；健康、就绪和指标端点也使用固定 span 名称。

## 指标与告警规则

在已有 MySQL 指标基础上新增：

```text
user_order_api_mysql_max_open_connections
```

它取自 `database/sql.DBStats.MaxOpenConnections`，用于计算连接池使用率。新增 Prometheus 规则文件，包含：

| 告警名 | 规则 | 持续时间 | 级别 |
| --- | --- | --- | --- |
| `UserOrderAPIUnavailable` | `up{job="user-order-api"} == 0` | 30 秒 | critical |
| `UserOrderAPI5xxResponses` | `sum(rate(user_order_api_http_requests_total{status=~"5.."}[5m])) > 0` | 1 分钟 | warning |
| `AuditEventsDropped` | `increase(user_order_api_audit_dropped_total[5m]) > 0` | 1 分钟 | warning |
| `MySQLConnectionPoolSaturated` | `user_order_api_mysql_in_use_connections / user_order_api_mysql_max_open_connections > 0.8` | 2 分钟 | warning |

每条规则有 `summary`、`description`、`severity` 标签和 `service=user-order-api` 标签。规则不包含用户、订单、Token 或错误文本。

Prometheus 配置 `rule_files` 并设置 `alerting.alertmanagers` 指向 Compose 网络中的 `alertmanager:9093`。

## Alertmanager 设计

Alertmanager 配置一个名为 `local` 的默认接收器。该接收器不向外部发送通知，仅接收、分组、去重、静默和展示告警。开发者可在 `http://localhost:9093` 查看 Pending、Firing、Silenced 状态。

本地规则验证使用 Alertmanager UI 和 Prometheus `Alerts` 页面。生产环境应在独立配置文件中添加组织批准的邮件、Slack 或事件管理接收器，不能将其密钥提交到仓库。

## Docker Compose 服务

新增服务：

| 服务 | 镜像职责 | 宿主机端口 |
| --- | --- | --- |
| `jaeger` | 接收 OTLP、存储并展示 Trace | `16686`（UI） |
| `alertmanager` | 接收并展示 Prometheus 告警 | `9093`（UI） |

`api` 依赖 `jaeger` 已启动，并通过环境变量导出 Trace；Prometheus 依赖 `alertmanager` 已启动。Trace 和告警数据使用开发用命名卷；正常停止使用 `docker compose down`，不执行 `docker compose down -v`。

## 验收标准

1. 未配置 OTLP 端点时，宿主机直接运行 API 仍能启动并服务请求。
2. `docker compose up --build -d` 后可访问 API `:8888`、Prometheus `:9090`、Jaeger `:16686` 和 Alertmanager `:9093`。
3. 调用 `/api/v1/health` 或受保护业务接口后，Jaeger 可按 `user-order-api` 服务名查到 HTTP span；启用 MySQL 的请求还可看到 MySQL 子 span。
4. 请求日志包含非空 `trace_id` 与 `span_id`，但不包含凭证或个人敏感信息。
5. Prometheus 规则加载成功，Alertmanager 出现在 Prometheus 的告警配置中。
6. 停止 API 容器超过 30 秒后，`UserOrderAPIUnavailable` 在 Prometheus 与 Alertmanager 中变为 firing；重新启动 API 后告警恢复。
7. `go test ./...`、`go vet ./...`、`go test -race ./...`、Compose 配置校验与 `promtool check rules` 全部通过。
