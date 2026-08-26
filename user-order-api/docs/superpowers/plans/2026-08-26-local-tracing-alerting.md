# 本地链路追踪与告警 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为本地 Compose API 补齐 OpenTelemetry + Jaeger 请求链路追踪，以及 Prometheus Rules + Alertmanager 告警闭环。

**Architecture:** 新建 `internal/platform/telemetry` 负责可关闭的 Trace Provider 和 HTTP Trace 包装器。`cmd/api` 在启动时创建它，将 Provider 显式注入 MySQL 和 HTTP；应用日志只从请求 Context 取得 trace/span ID。Compose 加入 Jaeger、Alertmanager 和 Prometheus 规则，保留现有 MySQL volume。

**Tech Stack:** Go 1.25、OpenTelemetry Go SDK、OTLP/gRPC、`otelhttp`、`github.com/XSAM/otelsql`、Jaeger、Prometheus v3、Alertmanager、Docker Compose。

**Spec:** `docs/superpowers/specs/2026-08-26-local-tracing-alerting-design.md`

## Global Constraints

- 未设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 时，`go run ./cmd/api` 不依赖 Jaeger；端点设置但初始化失败时进程启动失败。
- 绝不记录 DSN、密码、JWT、Cookie、Refresh Token、邮箱、SQL 参数、完整 SQL、用户 ID 或订单 ID。
- HTTP span 与指标路由必须使用固定模板，绝不使用真实 URL ID。
- Alertmanager 仅做本地展示/分组，不能添加邮件、Slack、PagerDuty 或外部通知地址。
- 不执行 `docker compose down -v`，不删除或重置 `user_order_api`；集成测试只允许 `user_order_api_test`。
- 用户将自行审核和提交：本次所有改动保持未提交，计划中的检查点替代提交步骤。

## 文件结构

| 文件 | 责任 |
| --- | --- |
| `internal/platform/telemetry/telemetry.go` | 创建、暴露、关闭 Trace Provider；无端点时使用 no-op。 |
| `internal/platform/telemetry/http.go` | 用 W3C `traceparent` 创建固定名称的 HTTP server span。 |
| `internal/platform/telemetry/telemetry_test.go` | Provider、HTTP 传播与安全 span 名测试。 |
| `cmd/api/config.go`、`config_test.go` | OTel 运行配置。 |
| `cmd/api/main.go` | 管理 Trace 生命周期及依赖注入。 |
| `internal/platform/database/mysql.go` | 通过 `otelsql` 打开受观测的 MySQL。 |
| `internal/platform/database/database_integration_test.go` | 专用测试库中的 SQL 子 span 验证。 |
| `internal/app/application.go`、`http_test.go` | 请求日志写入 `trace_id`、`span_id`。 |
| `internal/platform/observability/metrics.go`、`metrics_test.go` | MySQL 最大连接数 Gauge。 |
| `compose.yaml`、`docker/prometheus/*`、`docker/alertmanager/*` | Jaeger、规则与 Alertmanager 栈。 |
| `docs/local-docker-prometheus.md`、`README.md`、`implementation-roadmap.md` | 启动、验收、状态说明。 |

---

### Task 1: OTel 配置与可关闭 Provider

**Files:**

- Create: `internal/platform/telemetry/telemetry.go`
- Create: `internal/platform/telemetry/telemetry_test.go`
- Modify: `cmd/api/config.go`
- Modify: `cmd/api/config_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- `telemetry.Config{ServiceName string, OTLPGRPCEndpoint string, Insecure bool}`。
- `telemetry.New(context.Context, telemetry.Config) (*telemetry.Runtime, error)`。
- `Runtime.TracerProvider() trace.TracerProvider`、`Runtime.Shutdown(context.Context) error`。
- `serverConfig` 增加 `OTelServiceName`、`OTLPGRPCEndpoint`、`OTLPGRPCInsecure`。

- [x] **Step 1: 加入 OpenTelemetry 模块。**

```bash
go get go.opentelemetry.io/otel go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp github.com/XSAM/otelsql
go mod tidy
```

Expected: 仅新增 OTel API、SDK、OTLP、HTTP 和 SQL instrumentation，不引入 ORM 或业务框架。

- [x] **Step 2: 先写失败测试。**

在 `config_test.go` 新增：

```go
func TestLoadConfigParsesOpenTelemetrySettings(t *testing.T) {
	config, err := loadConfig(testEnvironment(map[string]string{
		"OTEL_SERVICE_NAME": "orders-local", "OTEL_EXPORTER_OTLP_ENDPOINT": "jaeger:4317", "OTEL_EXPORTER_OTLP_INSECURE": "true",
	}))
	if err != nil { t.Fatal(err) }
	if config.OTelServiceName != "orders-local" || config.OTLPGRPCEndpoint != "jaeger:4317" || !config.OTLPGRPCInsecure { t.Fatalf("OTel config = %+v", config) }
}

func TestLoadConfigRejectsInvalidOTLPInsecure(t *testing.T) {
	_, err := loadConfig(testEnvironment(map[string]string{"OTEL_EXPORTER_OTLP_INSECURE": "yes"}))
	if err == nil || err.Error() != "OTEL_EXPORTER_OTLP_INSECURE must be true or false" { t.Fatalf("error = %v", err) }
}
```

在 `telemetry_test.go` 测试空端点：`New(context.Background(), Config{ServiceName: "user-order-api"})` 不报错、`TracerProvider` 非 nil、`Shutdown` 返回 nil，且测试过程中没有连接网络。

- [x] **Step 3: 运行并确认 RED。**

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/platform/telemetry -run 'Test(LoadConfig.*OpenTelemetry|NewWithoutEndpoint)' -count=1
```

Expected: FAIL，原因是字段和 telemetry 包未实现，不能因 MySQL/JWT 配置失败。

- [x] **Step 4: 最小实现。**

`defaultServerConfig` 默认服务名为 `user-order-api`。端点和服务名均 `TrimSpace`；`OTEL_EXPORTER_OTLP_INSECURE` 非空时 `strconv.ParseBool`，失败返回精确错误。

`Runtime` 必须为：

```go
type Runtime struct { provider trace.TracerProvider; shutdown func(context.Context) error }
func New(ctx context.Context, config Config) (*Runtime, error)
func (r *Runtime) TracerProvider() trace.TracerProvider
func (r *Runtime) Shutdown(ctx context.Context) error
```

端点为空使用 `trace.NewNoopTracerProvider()` 和空 shutdown。端点非空使用 `otlptracegrpc.New(ctx, ...)`、`sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(config.ServiceName))))`；`Insecure` 为真时加入 `otlptracegrpc.WithInsecure()`；创建失败包装为 `create OTLP trace exporter: %w`。用 `sync.Once` 保证 shutdown 只运行一次。

- [x] **Step 5: GREEN 与检查点。**

```bash
gofmt -w cmd/api/config.go cmd/api/config_test.go internal/platform/telemetry
GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/platform/telemetry -count=1
git diff --check
```

Expected: PASS；不执行提交。

### Task 2: HTTP span、请求日志关联和应用启动注入

**Files:**

- Create: `internal/platform/telemetry/http.go`
- Modify: `internal/platform/telemetry/telemetry_test.go`
- Modify: `internal/app/application.go`
- Modify: `internal/app/http_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**

- `telemetry.HTTPHandler(next http.Handler, provider trace.TracerProvider) http.Handler`。
- `requestLogMiddleware` 输出 `trace_id` 和 `span_id`。
- `main` 先创建 Runtime，后将它用于 HTTP 与 MySQL。

- [x] **Step 1: 先写失败测试。**

在 `telemetry_test.go` 使用 SDK 内存 exporter：发送 `GET /api/v1/orders/42` 和请求头 `traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`，断言 ended span 名为 `GET /api/v1/orders/:id` 且 parent trace ID 为 `4bf92f3577b34da6a3ce929d0e0e4736`。

在 `http_test.go` 对 `requestLogMiddleware` 注入有效 `trace.SpanContext`，断言日志包含 `trace_id=<id>`、`span_id=<id>`，且不包含 Authorization header 值。

- [x] **Step 2: 运行并确认 RED。**

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/telemetry ./internal/app -run 'Test(HTTPHandler|RequestLog)' -count=1
```

Expected: FAIL，`HTTPHandler` 与日志字段尚不存在。

- [x] **Step 3: 最小实现。**

`HTTPHandler` 使用：

```go
otelhttp.NewHandler(next, "HTTP",
	otelhttp.WithTracerProvider(provider),
	otelhttp.WithPropagators(propagation.TraceContext{}),
	otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + observability.NormalizeRoute(r.URL.Path) }),
)
```

日志从 `trace.SpanContextFromContext(r.Context())` 读取有效 `traceID`/`spanID`，添加到既有 `logger.InfoContext`；不读取 header、cookie、body 或 query。`main.go` 在打开 DB 前创建 `telemetry.New`，defer 以 `config.ShutdownTimeout` 关闭 Runtime；将 `telemetry.HTTPHandler(application, runtime.TracerProvider())` 传给 `newHTTPServer`。确保 Trace middleware 位于应用 handler 外层，使日志取得 SpanContext。

- [x] **Step 4: GREEN、race 和检查点。**

```bash
gofmt -w cmd/api/main.go internal/app/application.go internal/app/http_test.go internal/platform/telemetry
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/telemetry ./internal/app -count=1
GOCACHE=/private/tmp/user-order-api-go-cache go test -race ./internal/platform/telemetry ./internal/app -count=1
git diff --check
```

Expected: PASS；真实 `42` 不得出现在 span 名。

### Task 3: MySQL 子 span 与最大连接数指标

**Files:**

- Modify: `internal/platform/database/mysql.go`
- Modify: `internal/platform/database/database_integration_test.go`
- Modify: `internal/platform/observability/metrics.go`
- Modify: `internal/platform/observability/metrics_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**

- `database.Open(ctx context.Context, dsn string, provider trace.TracerProvider) (*sql.DB, error)`。
- 导出 `user_order_api_mysql_max_open_connections`。

- [x] **Step 1: 先写失败测试。**

在 metrics 测试中以现有 fake DBStats 断言 exposition 含 `user_order_api_mysql_max_open_connections 10`。在 `MYSQL_TEST_DSN` 专用库集成测试中，创建 recording provider，调用 `Open`、`PingContext` 和一次 `QueryContext`；断言至少一个 SQL span 是父 trace 的子 span，span 属性不含测试 DSN 或 `app_password`。未设置 `MYSQL_TEST_DSN` 时跳过。

- [x] **Step 2: 运行并确认 RED。**

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/observability -run TestMetricsExposeMySQLMaxOpenConnections -count=1
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/database -run TestOpenProducesTraceForMySQLOperation -count=1
```

Expected: first FAIL because gauge absent; second FAIL because Open lacks Provider or SQL spans. Test DB unavailable is an environment blocker: do not replace its target.

- [x] **Step 3: 最小实现。**

用 `otelsql.Open("mysql", dsn, otelsql.WithTracerProvider(provider), otelsql.WithSpanOptions(otelsql.SpanOptions{DisableQuery: true}))` 替换 `sql.Open`；nil Provider 替换为 `trace.NewNoopTracerProvider()`。不启用 SQL commenter，不添加 query、DSN 或参数 attribute。保留原连接池大小、Ping timeout 和失败关闭。metrics 新增：

```go
prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "user_order_api", Name: "mysql_max_open_connections", Help: "Configured maximum open MySQL connections."}, func() float64 { return float64(db.Stats().MaxOpenConnections) })
```

生产 `database.Open` 传 `runtime.TracerProvider()`；测试显式传 no-op 或 recording provider。

- [x] **Step 4: GREEN。**

```bash
gofmt -w cmd/api/main.go internal/platform/database internal/platform/observability
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/database ./internal/platform/observability -count=1
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/database -count=1
```

Expected: PASS；不触碰 `user_order_api`。

### Task 4: Jaeger、规则和 Alertmanager Compose 栈

**Files:**

- Create: `docker/prometheus/alerts.yml`
- Create: `docker/alertmanager/alertmanager.yml`
- Modify: `compose.yaml`
- Modify: `docker/prometheus/prometheus.yml`
- Modify: `scripts/scripts_test.go`

**Interfaces:**

- 四条规则：`UserOrderAPIUnavailable`、`UserOrderAPI5xxResponses`、`AuditEventsDropped`、`MySQLConnectionPoolSaturated`。
- Prometheus 使用 `alertmanager:9093`；Jaeger 仅暴露 UI `16686`，Alertmanager 暴露 UI `9093`。

- [x] **Step 1: 先写失败的静态合同测试。**

`scripts/scripts_test.go` 读取 Compose、Prometheus 和 alerts 文件，逐个检查四个 `alert: <name>`，检查 `jaeger:`、`alertmanager:`、`OTEL_EXPORTER_OTLP_ENDPOINT: jaeger:4317`、`rule_files:`、`alertmanager:9093`。断言 Compose 不含 `down -v`。

- [x] **Step 2: 运行并确认 RED。**

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./scripts -run TestComposeIncludesTracingAndAlerting -count=1
```

Expected: FAIL，因为服务和规则尚不存在。

- [x] **Step 3: 最小配置实现。**

`alerts.yml` 必须含如下精确表达式和持续时间：

```yaml
- alert: UserOrderAPIUnavailable
  expr: up{job="user-order-api"} == 0
  for: 30s
- alert: UserOrderAPI5xxResponses
  expr: sum(rate(user_order_api_http_requests_total{status=~"5.."}[5m])) > 0
  for: 1m
- alert: AuditEventsDropped
  expr: increase(user_order_api_audit_dropped_total[5m]) > 0
  for: 1m
- alert: MySQLConnectionPoolSaturated
  expr: user_order_api_mysql_in_use_connections / user_order_api_mysql_max_open_connections > 0.8
  for: 2m
```

每条加 `service: user-order-api`、severity（首条 critical，余下 warning）、`summary`、`description`。Alertmanager 配置 receiver `local`，`group_by: [alertname, service]`，不含任何 webhook 或外发地址。Prometheus 加 `rule_files: ["/etc/prometheus/alerts.yml"]` 和 target `alertmanager:9093`。Compose 加入 Jaeger all-in-one（`16686:16686`，OTLP 仅容器网络）和 Alertmanager（只读配置、`9093:9093`）；API 加三项 OTel 环境变量并依赖 Jaeger；Prometheus 挂载 rules 并依赖 Alertmanager。不能更改 MySQL volume 名称。

- [x] **Step 4: GREEN 与配置校验。**

```bash
docker compose config
docker run --rm -v "$PWD/docker/prometheus:/etc/prometheus:ro" prom/prometheus:v3.5.0 promtool check config /etc/prometheus/prometheus.yml
docker run --rm -v "$PWD/docker/prometheus:/etc/prometheus:ro" prom/prometheus:v3.5.0 promtool check rules /etc/prometheus/alerts.yml
GOCACHE=/private/tmp/user-order-api-go-cache go test ./scripts -count=1
git diff --check
```

Expected: PASS；仅检查配置，不停止或删除任何数据卷。

### Task 5: 文档、端到端验收与审核检查点

**Files:**

- Modify: `docs/local-docker-prometheus.md`
- Modify: `README.md`
- Modify: `docs/implementation-roadmap.md`
- Modify: `scripts/scripts_test.go`

**Interfaces:**

- 用户通过 API `:8888`、Prometheus `:9090`、Jaeger `:16686`、Alertmanager `:9093` 验收。
- 阶段 7 标记为完成，生产通知渠道明确为运维后续配置。

- [x] **Step 1: 先写失败文档合同测试。**

测试读取 README 和本地可观测性文档，断言含 `http://localhost:16686`、`http://localhost:9093`、`trace_id`、`UserOrderAPIUnavailable`、`docker compose down -v` 禁止说明。

- [x] **Step 2: 运行并确认 RED。**

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./scripts -run TestObservabilityDocumentationIncludesTracingAndAlerts -count=1
```

Expected: FAIL，文档尚未包含 Jaeger/Alertmanager。

- [x] **Step 3: 更新文档。**

在 `docs/local-docker-prometheus.md` 写入拓扑、四个地址、Jaeger 中筛选 `user-order-api`、Trace 与日志 `trace_id` 关联、Prometheus Alerts 页及 Alertmanager 页。加入以下安全、可恢复验收：

```bash
docker compose up --build -d
curl http://localhost:8888/api/v1/health
docker compose stop api
# 等待至少 45 秒，在 Prometheus Alerts 和 Alertmanager 查看 UserOrderAPIUnavailable
docker compose start api
```

明确只允许停止/启动 `api`，不得 `down -v`；恢复后等待下一次 scrape 让告警 resolved。README 增加 UI 地址、三个 OTel 环境变量及文档链接。路线图把阶段 7 标记完成，并注明生产通知渠道仍待运维确认。

- [x] **Step 4: 完整自动与实际环境验证。**

```bash
gofmt -w cmd/api internal/app internal/platform
go test ./...
go vet ./...
go test -race ./...
docker compose up --build -d
docker compose ps
curl --fail http://localhost:8888/healthz
curl --fail http://localhost:8888/readyz
curl --fail http://localhost:8888/api/v1/health
curl --fail http://localhost:9090/-/ready
curl --fail http://localhost:16686
curl --fail http://localhost:9093/-/ready
```

在 Jaeger 确认 `GET /api/v1/health` span，并用一个访问 MySQL 的注册/登录请求确认 SQL 子 span。之后 `docker compose stop api`，等待 45 秒，确认 unavailable 告警在 Prometheus 与 Alertmanager firing；`docker compose start api`，等待 scrape 后确认 resolved。

- [x] **Step 5: 最终审核检查点。**

```bash
git diff --check
git status --short
git diff --stat
```

Expected: 仅有可审核的阶段 7 追踪/告警改动；不执行 `git add`、`git commit`、`git push`、`docker compose down -v` 或真实库删除动作。
