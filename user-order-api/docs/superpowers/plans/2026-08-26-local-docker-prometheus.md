# Local Docker and Prometheus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the API, MySQL and Prometheus locally in Docker, and expose safe health and metrics endpoints that make real API, database-pool and audit-queue behavior observable.

**Architecture:** Add a small `internal/platform/observability` package that owns a private Prometheus registry, bounded-label HTTP middleware and pull-time collectors. `internal/app` composes this package with its existing audit logger and, in production, the existing `*sql.DB`; it adds root health endpoints while preserving `/api/v1/health`. Docker Compose builds the same Go API into an `api` service and configures Prometheus to scrape `api:8888/metrics` every 15 seconds.

**Tech Stack:** Go 1.25.3, standard library `net/http` and `database/sql`, `github.com/prometheus/client_golang`, Docker Compose, MySQL 8.4, Prometheus.

**Spec:** `docs/superpowers/specs/2026-08-26-local-docker-prometheus-design.md`

## Global Constraints

- Preserve `/api/v1/health` and the existing API contract.
- Do not include passwords, JWTs, refresh tokens, email addresses, IDs or error text in metric labels.
- Metrics route labels must be a fixed finite set; dynamic URL path segments use `:id` templates.
- `/healthz` must not contact MySQL; `/readyz` must return `503` without exposing database error details when MySQL is unavailable.
- Local Compose preserves MySQL host mapping `3307:3306` for Navicat; production deployment must not expose MySQL.
- No database reset or destructive command may target `user_order_api`; integration tests use only `user_order_api_test` through `MYSQL_TEST_DSN`.
- Run `GOCACHE=/private/tmp/user-order-api-go-cache go test ./...`, `go vet ./...`, and `go test -race ./...` before each implementation commit.

---

### Task 1: Add a bounded Prometheus metrics package and audit queue statistics

**Files:**
- Create: `internal/platform/observability/metrics.go`
- Create: `internal/platform/observability/metrics_test.go`
- Modify: `internal/platform/audit/audit.go`
- Modify: `internal/platform/audit/audit_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `audit.AsyncLogger.Dropped() uint64` and `database/sql.DB.Stats() sql.DBStats`.
- Produces: `observability.New(db DatabaseStats, audit AuditStats) (*Metrics, *prometheus.Registry)`, `(*Metrics).Middleware(http.Handler) http.Handler`, `(*Metrics).Handler() http.Handler`, `(*AsyncLogger).Pending() int`.
- `DatabaseStats` is `interface { Stats() sql.DBStats }`; `AuditStats` is `interface { Pending() int; Dropped() uint64 }`.

- [ ] **Step 1: Add the failing audit queue visibility test.**

Add this test beside the existing queue-full test in `internal/platform/audit/audit_test.go`; it deliberately blocks the worker so one item remains buffered:

```go
func TestAsyncLoggerReportsPendingEvents(t *testing.T) {
	worker := newBlockingHandler()
	logger := newAsyncLogger(slog.New(worker), 1)
	logger.Record(context.Background(), "first", nil)
	<-worker.started
	logger.Record(context.Background(), "second", nil)

	if got := logger.Pending(); got != 1 {
		t.Fatalf("Pending() = %d, want 1", got)
	}

	close(worker.release)
	closeAuditLogger(t, logger)
}
```

- [ ] **Step 2: Run the audit test and verify the missing API failure.**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/audit -run TestAsyncLoggerReportsPendingEvents -count=1`

Expected: compilation failure because `AsyncLogger.Pending` does not exist.

- [ ] **Step 3: Implement the minimal audit statistic.**

Add this method to `internal/platform/audit/audit.go` beside `Dropped`:

```go
func (l *AsyncLogger) Pending() int {
	return len(l.events)
}
```

Do not expose the channel itself; the public method only supplies a point-in-time gauge value.

- [ ] **Step 4: Run the audit package tests.**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/audit -count=1`

Expected: PASS.

- [ ] **Step 5: Add failing metrics tests.**

Create `internal/platform/observability/metrics_test.go` with fakes for database and audit statistics. Cover all three behaviors below:

```go
func TestNormalizeRouteReplacesDynamicSegments(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/api/v1/orders/42", "/api/v1/orders/:id"},
		{"/api/v1/orders/42/pay", "/api/v1/orders/:id/pay"},
		{"/api/v1/users/9", "/api/v1/users/:id"},
		{"/api/v1/auth/login", "/api/v1/auth/login"},
	} {
		if got := NormalizeRoute(tc.path); got != tc.want {
			t.Fatalf("NormalizeRoute(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestMiddlewareRecordsFixedRouteAndStatus(t *testing.T) {
	metrics, registry := New(nil, fakeAuditStats{})
	handler := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/orders/42", nil))

	got := gatherMetric(t, registry, "user_order_api_http_requests_total", map[string]string{
		"method": "POST", "route": "/api/v1/orders/:id", "status": "201",
	})
	if got != 1 { t.Fatalf("request count = %v, want 1", got) }
}

func TestRegistryExportsDatabaseAndAuditStatistics(t *testing.T) {
	_, registry := New(fakeDatabaseStats{open: 4, inUse: 2, idle: 2}, fakeAuditStats{pending: 3, dropped: 1})
	if got := gatherMetric(t, registry, "user_order_api_mysql_open_connections", nil); got != 4 { t.Fatalf("open = %v", got) }
	if got := gatherMetric(t, registry, "user_order_api_audit_queue_pending", nil); got != 3 { t.Fatalf("pending = %v", got) }
	if got := gatherMetric(t, registry, "user_order_api_audit_dropped_total", nil); got != 1 { t.Fatalf("dropped = %v", got) }
}
```

Implement `gatherMetric` using `registry.Gather()` and inspect the matching `dto.MetricFamily`; it must fail if a required label/value pair is absent. Define `fakeDatabaseStats.Stats() sql.DBStats` and `fakeAuditStats.Pending()/Dropped()` in the test file.

- [ ] **Step 6: Run the observability tests and verify the intended failure.**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/observability -count=1`

Expected: package/build failure because `observability` does not exist and `github.com/prometheus/client_golang` is not required yet.

- [ ] **Step 7: Add the Prometheus dependency and minimal metrics implementation.**

Run `go get github.com/prometheus/client_golang/prometheus` to add the official client library.

In `metrics.go`, implement the exported interfaces and API:

```go
type DatabaseStats interface { Stats() sql.DBStats }
type AuditStats interface { Pending() int; Dropped() uint64 }

type Metrics struct {
	requests  *prometheus.CounterVec
	durations *prometheus.HistogramVec
	inFlight  prometheus.Gauge
}

func New(db DatabaseStats, audit AuditStats) (*Metrics, *prometheus.Registry)
func (m *Metrics) Middleware(next http.Handler) http.Handler
func (m *Metrics) Handler() http.Handler
func NormalizeRoute(path string) string
```

`New` creates a fresh `prometheus.NewRegistry()`, registers Go/process collectors, then registers:

```go
prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "user_order_api", Name: "http_requests_total",
	Help: "Total HTTP requests served.",
}, []string{"method", "route", "status"})

prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "user_order_api", Name: "http_request_duration_seconds",
	Help: "HTTP request duration in seconds.",
}, []string{"method", "route", "status"})
```

Register the in-flight gauge with no labels. If `db != nil`, register `prometheus.NewGaugeFunc` values named `mysql_open_connections`, `mysql_in_use_connections`, and `mysql_idle_connections`. Always register audit `audit_queue_pending` and `audit_dropped_total` with `NewGaugeFunc`/`NewCounterFunc`. `Handler` returns `promhttp.HandlerFor(registry, promhttp.HandlerOpts{})`.

`Middleware` must increment in-flight before calling `next`, decrement with `defer`, capture the final response status, and observe both the counter and duration after `next` returns. Use a private response writer wrapper equivalent to the existing `statusRecorder`. `NormalizeRoute` must only return known fixed routes or the templates `/api/v1/users/:id`, `/api/v1/orders/:id`, `/api/v1/orders/:id/pay`, `/api/v1/orders/:id/cancel`; unknown paths return `/unknown`.

- [ ] **Step 8: Run metrics tests and the full package test suite.**

Run:

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/observability -count=1
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/audit -count=1
```

Expected: PASS. Confirm metric exposition contains no literal `42` route label.

- [ ] **Step 9: Commit the isolated metrics change.**

```bash
git add go.mod go.sum internal/platform/audit internal/platform/observability
git commit -m "feat: 添加 Prometheus 应用指标"
```

### Task 2: Compose health, readiness and metrics into the application

**Files:**
- Modify: `internal/app/application.go`
- Modify: `internal/app/composition.go`
- Modify: `internal/app/test_helpers_test.go`
- Modify: `internal/app/http_test.go`

**Interfaces:**
- Consumes: `observability.New`, `(*Metrics).Middleware`, `(*Metrics).Handler`, `*sql.DB.PingContext`, and `audit.AsyncLogger` as `AuditStats`.
- Produces: unauthenticated root endpoints `/healthz`, `/readyz`, `/metrics`; production application metrics that read the actual SQL pool.
- Add a private app-level interface: `type readinessChecker interface { PingContext(context.Context) error }`.

- [ ] **Step 1: Add failing HTTP contract tests.**

In `internal/app/http_test.go`, add tests that construct the existing memory application and assert:

```go
func TestHealthzIsLiveWithoutDatabase(t *testing.T) {
	server := newTestServer(t)
	get(t, server, "/healthz", http.StatusOK)
}

func TestReadyzReturnsServiceUnavailableWhenDependencyFails(t *testing.T) {
	server := newServerWithReadiness(t, failingReadiness{})
	body := get(t, server, "/readyz", http.StatusServiceUnavailable)
	if string(body) != "{\"status\":\"not_ready\"}\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestMetricsEndpointUsesPrometheusContentType(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics response = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
```

Implement test-only `failingReadiness` and a `newServerWithReadiness` helper that builds the normal dependency graph but injects it.

- [ ] **Step 2: Run the new endpoint tests and verify they fail.**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/app -run 'Test(Healthz|Readyz|Metrics)' -count=1`

Expected: `/healthz`, `/readyz` and `/metrics` return the current route-not-found response, and the helper cannot yet inject readiness.

- [ ] **Step 3: Extend application dependencies and register the endpoints.**

Add private fields to `Application` for `readiness readinessChecker` and `metrics *observability.Metrics`. Extend `Dependencies` with:

```go
Readiness     readinessChecker
DatabaseStats observability.DatabaseStats
```

In `NewWithDependencies`, create the audit logger first, then call `observability.New(deps.DatabaseStats, auditLogger)`. Register `GET /healthz` and `GET /readyz` on the outer mux before its catch-all route. `/healthz` writes `{"status":"ok"}`. `/readyz` calls `PingContext` with a two-second child context; a nil checker is ready for memory tests, a non-nil error writes `503 {"status":"not_ready"}`. Register `GET /metrics` with `metrics.Handler()`; non-GET behavior comes from `promhttp` and is not part of the public API contract.

Wrap the existing request-ID/log/recovery/security handler stack with `metrics.Middleware` so API and health endpoints emit metrics. Keep `/api/v1/health` unchanged.

In `NewProduction`, populate both `Readiness` and `DatabaseStats` with the provided `*sql.DB`. In `NewMemory`, leave both nil. Update test helpers to create dependencies through the extended structure.

- [ ] **Step 4: Run focused application tests.**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/app -count=1`

Expected: PASS, including existing authentication, authorization and order flow tests.

- [ ] **Step 5: Add the integration assertion for the real application composition.**

Extend the existing MySQL integration branch in `internal/app/http_test.go` to call `/readyz` against `NewProduction` and assert 200. It must use `MYSQL_TEST_DSN` through the existing test database helper and must not alter `user_order_api`.

- [ ] **Step 6: Run all HTTP and MySQL composition tests.**

Run:

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/app -count=1
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/app -run MySQL -count=1
```

Expected: PASS. If `MYSQL_TEST_DSN` is not available, run only the first command and state that the MySQL test was skipped by its existing guard.

- [ ] **Step 7: Commit application composition.**

```bash
git add internal/app
git commit -m "feat: 添加健康检查与指标端点"
```

### Task 3: Add the API Docker image and local Prometheus stack

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`
- Create: `docker/prometheus/prometheus.yml`
- Modify: `compose.yaml`

**Interfaces:**
- Consumes: the API binary built from `./cmd/api`, `MYSQL_DSN`, `JWT_SIGNING_KEY`, `/healthz`, and `/metrics`.
- Produces: `docker compose up --build` starts `api`, `mysql`, and `prometheus`; Prometheus scrapes `http://api:8888/metrics`.

- [ ] **Step 1: Add Compose structure checks before implementation.**

Create a temporary command checklist (do not add a test that requires Docker to ordinary `go test`):

```bash
docker compose config --quiet
docker compose config | rg 'api:|prometheus:|3307:3306|8888:8888|9090:9090'
```

Before editing, the second command must fail because `api` and `prometheus` do not exist.

- [ ] **Step 2: Create the multi-stage Dockerfile and ignore rules.**

Create `Dockerfile` with this behavior:

```dockerfile
FROM golang:1.25.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/user-order-api ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
COPY --from=build /out/user-order-api /usr/local/bin/user-order-api
USER app
EXPOSE 8888
ENTRYPOINT ["/usr/local/bin/user-order-api"]
```

Create `.dockerignore` excluding `.git`, `.env`, `tmp`, coverage files, local binaries, `.DS_Store`, and `mysql_data`; do not exclude required Go source, migrations or `go.sum`.

- [ ] **Step 3: Add Prometheus configuration and Compose services.**

Create `docker/prometheus/prometheus.yml`:

```yaml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: user-order-api
    metrics_path: /metrics
    static_configs:
      - targets: ["api:8888"]
```

Extend `compose.yaml` without removing the `mysql` port mapping, volume, initialization SQL or health check:

```yaml
  api:
    build: .
    environment:
      MYSQL_DSN: app:app_password@tcp(mysql:3306)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC
      JWT_SIGNING_KEY: local-development-key-must-be-at-least-32-bytes
      AUTH_COOKIE_SECURE: "false"
    depends_on:
      mysql:
        condition: service_healthy
    ports:
      - "8888:8888"
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8888/healthz"]
      interval: 10s
      timeout: 3s
      retries: 10

  prometheus:
    image: prom/prometheus
    command: ["--config.file=/etc/prometheus/prometheus.yml", "--storage.tsdb.path=/prometheus"]
    volumes:
      - ./docker/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - prometheus_data:/prometheus
    depends_on:
      api:
        condition: service_healthy
    ports:
      - "9090:9090"

volumes:
  mysql_data:
  prometheus_data:
```

- [ ] **Step 4: Verify rendered Compose configuration and build the image.**

Run:

```bash
docker compose config --quiet
docker compose build api
```

Expected: both commands exit 0. Inspect the rendered `MYSQL_DSN` and confirm it targets `mysql:3306`, not host `127.0.0.1:3307`.

- [ ] **Step 5: Start the local stack and verify service health without deleting data.**

Run:

```bash
docker compose up --build -d
curl --fail --silent http://localhost:8888/healthz
curl --fail --silent http://localhost:8888/readyz
curl --fail --silent http://localhost:8888/api/v1/health
docker compose ps
```

Expected: all three API calls return `{"status":"ok"}` and all three services are running/healthy. Do not execute `docker compose down -v` because it would delete local MySQL and Prometheus volumes.

- [ ] **Step 6: Commit container delivery files.**

```bash
git add Dockerfile .dockerignore compose.yaml docker/prometheus/prometheus.yml
git commit -m "feat: 添加本地 Docker 与 Prometheus 栈"
```

### Task 4: Document operational use and perform the complete acceptance pass

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/openapi.yaml`

**Interfaces:**
- Consumes: the endpoints and stack delivered in Tasks 1–3.
- Produces: reproducible local operating instructions and documented health/metrics behavior.

- [ ] **Step 1: Add failing documentation acceptance commands.**

Before writing docs, record the exact commands that must be explained and executed:

```bash
docker compose up --build -d
curl http://localhost:8888/healthz
curl http://localhost:8888/readyz
curl http://localhost:8888/metrics | rg 'user_order_api_http_requests_total'
curl 'http://localhost:9090/api/v1/query?query=user_order_api_http_requests_total'
```

Expected before Tasks 1–3: `/healthz`, `/readyz`, `/metrics`, and Prometheus query are unavailable.

- [ ] **Step 2: Document local Compose operations and safety boundaries.**

Update `README.md` with:

- `docker compose up --build -d`, `docker compose ps`, `docker compose logs -f api`, and non-destructive stop command `docker compose down`;
- endpoint semantics for `/healthz`, `/readyz`, `/metrics`, and Prometheus at `http://localhost:9090`;
- the four PromQL examples from the spec;
- explicit warning that `docker compose down -v` deletes local MySQL/Prometheus volumes and must not be used for normal development;
- explanation that MySQL remains reachable from Navicat at `127.0.0.1:3307`, while the API uses internal `mysql:3306`.

Update `docs/architecture.md` with the container topology, root endpoints and the fixed-label rule. Add the three operational endpoints to `docs/openapi.yaml` only if the project intends OpenAPI to enumerate operational endpoints; otherwise add an explicit top-level note that they are infrastructure endpoints outside the business API contract. Choose the latter unless the existing OpenAPI document already lists operational endpoints.

- [ ] **Step 3: Execute the complete automated verification.**

Run:

```bash
GOCACHE=/private/tmp/user-order-api-go-cache go test ./...
GOCACHE=/private/tmp/user-order-api-go-cache go vet ./...
GOCACHE=/private/tmp/user-order-api-go-cache go test -race ./...
docker compose config --quiet
```

Expected: every command exits 0. If MySQL integration tests are skipped because `MYSQL_TEST_DSN` is unset, run them separately with only the dedicated `user_order_api_test` DSN.

- [ ] **Step 4: Execute end-to-end metric collection verification.**

With the Compose stack running, request the non-mutating health endpoint twice, wait at least 15 seconds for one scrape cycle, then run:

```bash
curl --fail --silent http://localhost:8888/api/v1/health
curl --fail --silent http://localhost:8888/api/v1/health
sleep 16
curl --fail --silent 'http://localhost:9090/api/v1/query?query=user_order_api_http_requests_total'
curl --fail --silent 'http://localhost:9090/api/v1/query?query=user_order_api_mysql_open_connections'
curl --fail --silent 'http://localhost:9090/api/v1/query?query=user_order_api_audit_queue_pending'
```

Expected: Prometheus replies with `status:"success"`; HTTP metrics include route `/api/v1/health`, and no metric label contains an ID, email, token or credential.

- [ ] **Step 5: Commit documentation and acceptance updates.**

```bash
git add README.md docs/architecture.md docs/openapi.yaml
git commit -m "docs: 补充本地监控与容器运行说明"
```

## Plan self-review

- Spec coverage: Task 1 delivers the private registry, HTTP/database/audit metrics and finite labels; Task 2 delivers liveness/readiness/metrics endpoints; Task 3 delivers Docker image, Compose and scrape configuration; Task 4 delivers documentation and all required automated and live acceptance checks.
- Placeholder scan: no unresolved placeholders or deferred implementation steps remain; every task contains an exact target, test command and expected outcome.
- Type consistency: `DatabaseStats`, `AuditStats`, `Metrics`, `Pending`, `Readiness`, and `/healthz`/`/readyz`/`/metrics` use the same names throughout all tasks.
