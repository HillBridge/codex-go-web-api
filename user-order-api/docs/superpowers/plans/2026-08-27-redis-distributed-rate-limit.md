# Redis Distributed Rate Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将登录、Refresh 和普通 API 的限流计数从单进程内存扩展为可选的 Redis 共享计数，使多个 API 实例使用同一个固定一分钟窗口，并保留未配置 Redis 时的本地开发模式。

**Architecture:** 在 `internal/platform/security` 定义 `CounterStore` 抽象，保留基于互斥锁的内存实现并新增基于 Redis Lua `INCR + EXPIRE` 的实现。限流中间件只负责路由类别、客户端 IP 和 HTTP 响应；`cmd/api` 在启动阶段创建并 Ping Redis，`internal/app` 通过依赖注入使用计数器。Redis 运行时故障返回明确的 503，不降级为每个实例独立的内存计数。

**Tech Stack:** Go 1.25.3、`github.com/redis/go-redis/v9`、Redis 7.4 Alpine、Docker Compose、现有 `net/http`、Prometheus 指标和 shell 验收脚本。

**Spec:** `docs/superpowers/specs/2026-08-27-redis-distributed-rate-limit-design.md`

## Global Constraints

- 未设置 `REDIS_ADDR` 时必须保持现有内存限流和宿主机直接运行行为。
- 设置 `REDIS_ADDR` 后 API 启动阶段必须 Ping Redis；Ping 失败时进程启动失败。
- Redis 运行时不可用时只返回 `503 RATE_LIMIT_BACKEND_UNAVAILABLE`，不得静默回退到内存计数。
- Redis Lua 脚本必须原子执行 `INCR` 与首次 `EXPIRE`，窗口固定为一分钟。
- Redis Key 只能包含服务名、环境名、限流类别和客户端 IP，不得包含邮箱、用户 ID、订单 ID、Token 或请求体。
- 用户、订单和 Refresh 会话仍以 MySQL 为事实来源；本次不迁移业务数据、不新增缓存、锁、MQ 或微服务拆分。
- 本地验证只允许使用 `user_order_api_test` 做集成测试；不得删除或重置 `user_order_api`，不得执行 `docker compose down -v`。
- 用户要求自行审核和提交代码；执行过程中不执行 `git add`、`git commit`、`git push`。

## File Map

- Create: `internal/platform/security/counter_store.go` — `CounterStore` 接口、内存计数器和 Redis Lua 计数器。
- Modify: `internal/platform/security/ratelimit.go` — 注入计数器、构造 Redis Key、处理存储错误和 503。
- Modify: `internal/platform/security/ratelimit_test.go` — 共享计数器、运行时故障和 Key 边界测试。
- Modify: `internal/platform/httpx/httpx.go` — 添加可复用的 503 错误构造器。
- Modify: `internal/app/application.go` — 将限流计数器和 Redis 环境名传入中间件。
- Modify: `internal/app/composition.go` — 在应用配置和依赖组合根中传递限流存储。
- Modify: `internal/app/test_helpers_test.go` — 保持内存测试显式使用内存计数器默认值。
- Modify: `cmd/api/config.go` — 读取 `REDIS_ADDR` 和 `REDIS_ENVIRONMENT`。
- Modify: `cmd/api/config_test.go` — Redis 配置默认值、解析和非法值测试。
- Modify: `cmd/api/main.go` — 启动时创建/验证 Redis 计数器并在退出时关闭连接。
- Modify: `go.mod`, `go.sum` — 引入 go-redis v9。
- Modify: `compose.yaml` — 增加 Redis 服务、健康检查、数据卷和 API 依赖。
- Create: `scripts/redis-rate-limit-smoke.sh` — 两个 API 实例共享限流窗口的非破坏性验收脚本。
- Modify: `scripts/scripts_test.go` — Compose、脚本和安全约束静态测试。
- Modify: `docs/stage8-performance-baseline.md` — Redis 模式的启动、故障和双实例验证说明。
- Modify: `docs/architecture.md`, `docs/implementation-roadmap.md`, `README.md` — 更新架构、阶段状态和运行入口。

---

### Task 1: Add the counter-store abstraction and Redis implementation

**Files:**
- Create: `internal/platform/security/counter_store.go`
- Modify: `go.mod`, `go.sum`
- Test: `internal/platform/security/counter_store_test.go`

**Interfaces:**
- Produces `type CounterStore interface { Increment(context.Context, string, time.Duration) (int64, time.Duration, error) }`.
- Produces `NewMemoryCounterStore(now func() time.Time) CounterStore` for the existing in-process behavior.
- Produces `NewRedisCounterStore(ctx context.Context, addr, environment string) (*RedisCounterStore, error)`; the constructor must Ping before returning.
- Produces `(*RedisCounterStore).Close() error`.

- [ ] **Step 1: Write failing unit tests for the interface and Redis error contract**

```go
func TestMemoryCounterStoreReturnsCountAndTTL(t *testing.T) {
	clock := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	store := NewMemoryCounterStore(func() time.Time { return clock })
	count, ttl, err := store.Increment(context.Background(), "key", time.Minute)
	if err != nil || count != 1 || ttl != time.Minute { t.Fatalf("got count=%d ttl=%v err=%v", count, ttl, err) }
}

func TestRedisCounterStoreFailsPingForUnavailableAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := NewRedisCounterStore(ctx, "127.0.0.1:1", "test")
	if err == nil { t.Fatal("NewRedisCounterStore() error = nil, want ping failure") }
}
```

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./internal/platform/security -run 'TestMemoryCounterStoreReturnsCountAndTTL|TestRedisCounterStoreFailsPingForUnavailableAddress' -count=1`

Expected: FAIL because the counter-store types and constructors do not yet exist.

- [ ] **Step 3: Add the Redis dependency and implement the minimal stores**

Run: `go get github.com/redis/go-redis/v9`

Implement `counter_store.go` with these exact behaviors:

```go
var fixedWindowScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return {current, redis.call('TTL', KEYS[1])}
`)
```

The memory store must use a mutex and reset its bucket when the clock reaches the window. The Redis store must use `redis.NewClient`, call `Ping(ctx)` in the constructor, run the script with `window/time.Second`, convert the two returned integers to `(count, ttl)`, and close the client on shutdown. Wrap errors with context but never include the Redis password or full connection details in HTTP responses.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run: `go test ./internal/platform/security -run 'TestMemoryCounterStoreReturnsCountAndTTL|TestRedisCounterStoreFailsPingForUnavailableAddress' -count=1`

Expected: PASS.

### Task 2: Refactor the rate limiter to use shared counters

**Files:**
- Modify: `internal/platform/security/ratelimit.go`
- Modify: `internal/platform/security/ratelimit_test.go`
- Modify: `internal/platform/httpx/httpx.go`
- Create: `internal/platform/security/counter_store_test.go` (if not created in Task 1)

**Interfaces:**
- Produces `NewRateLimiterWithStore(limits Limits, now func() time.Time, trustedProxyCIDRs []netip.Prefix, store CounterStore, environment string) *RateLimiter`.
- Keeps `NewRateLimiter` and `NewRateLimiterWithTrustedProxies` source-compatible; both use the memory store.
- `Middleware` returns 429 `RATE_LIMITED` with `Retry-After` when the counter exceeds the class limit, and 503 `RATE_LIMIT_BACKEND_UNAVAILABLE` when `CounterStore.Increment` fails.

- [ ] **Step 1: Write failing tests for shared counters and backend errors**

Add a deterministic fake `CounterStore` in the test package that records keys and can return a configured error. Verify two independently constructed limiters sharing that store reject the second request when `APIPerMinute` is 1. Verify the error path returns status 503, JSON code `RATE_LIMIT_BACKEND_UNAVAILABLE`, and does not expose the fake error text. Verify a request from `203.0.113.10:1234` produces a key containing `user-order-api:test:rate:api:203.0.113.10` and no credential-like values.

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./internal/platform/security -run 'TestRateLimiterSharesCounterStore|TestRateLimiterReturnsBackendUnavailable|TestRateLimiterKeyContainsOnlyRoutingIdentity' -count=1`

Expected: FAIL because the new constructor, injected store and 503 path do not exist.

- [ ] **Step 3: Implement injected counting and error handling**

Refactor `RateLimiter` so it stores a `CounterStore` and environment. Build keys exactly as `user-order-api:<environment>:rate:<class>:<client-ip>`. `allow` must call `Increment(r.Context(), key, time.Minute)`, compare `count` to the selected limit, and derive `Retry-After` from the returned TTL rounded up to at least one second. If the store returns an error, write `httpx.ServiceUnavailableCode("RATE_LIMIT_BACKEND_UNAVAILABLE", "rate limit backend unavailable")` and return without calling the next handler. Keep trusted-proxy handling unchanged.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run: `go test ./internal/platform/security -run 'TestRateLimiter' -count=1`

Expected: PASS, including the original sixth-login and trusted-forwarded-for tests.

### Task 3: Wire Redis configuration through composition and startup

**Files:**
- Modify: `internal/app/application.go`
- Modify: `internal/app/composition.go`
- Modify: `internal/app/test_helpers_test.go`
- Modify: `cmd/api/config.go`
- Modify: `cmd/api/config_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- `serverConfig` gains `RedisAddr string` and `RedisEnvironment string`; default environment is `local`.
- `app.Config` gains `RateLimitStore security.CounterStore` and `RateLimitEnvironment string`.
- `app.Dependencies` gains the same store/environment fields.

- [ ] **Step 1: Write failing configuration and composition tests**

Extend `TestLoadConfigParsesAuthSecuritySettings` with `REDIS_ADDR=redis:6379` and `REDIS_ENVIRONMENT=staging`, then assert both values. Add a test asserting the default environment is `local` and whitespace around `REDIS_ADDR` is trimmed. Add a test rejecting `REDIS_ENVIRONMENT` containing whitespace. Add an application test using a fake shared store through `app.Config` and assert two requests are counted by the injected store.

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./cmd/api ./internal/app -run 'TestLoadConfig|TestApplicationUsesInjectedRateLimitStore' -count=1`

Expected: FAIL because the new configuration and dependency fields are absent.

- [ ] **Step 3: Implement configuration and lifecycle wiring**

Parse `REDIS_ADDR` with `strings.TrimSpace`. Parse `REDIS_ENVIRONMENT` with `strings.TrimSpace`; use `local` when empty and return `REDIS_ENVIRONMENT must not contain whitespace` for invalid values. In `NewWithDependencies`, construct the limiter using the injected store and environment; nil store must select the memory implementation. In `main`, before constructing the application, create a Redis store only when `config.RedisAddr != ""`, using a ten-second startup context. On constructor/Ping error, log fatal and exit before listening. Defer `Close` with the configured shutdown timeout and pass the store/environment into `app.Config`. Do not make `NewProduction` itself create a network client; startup ownership remains in `cmd/api`.

- [ ] **Step 4: Run the focused tests to verify they pass**

Run: `go test ./cmd/api ./internal/app -run 'TestLoadConfig|TestApplicationUsesInjectedRateLimitStore' -count=1`

Expected: PASS.

### Task 4: Add Redis to Docker Compose and dependency metadata

**Files:**
- Modify: `compose.yaml`
- Modify: `go.mod`, `go.sum`
- Modify: `scripts/scripts_test.go`

**Interfaces:**
- Compose exposes Redis only to the internal Compose network at `redis:6379`; no host port is required.
- API receives `REDIS_ADDR: redis:6379` and `REDIS_ENVIRONMENT: local` by default.

- [ ] **Step 1: Write failing static Compose tests**

Extend `TestComposeIncludesTracingAndAlerting` or add a focused test asserting `redis:`, `redis:7.4-alpine`, `REDIS_ADDR: redis:6379`, `redis-cli ping`, `redis_data`, and an API `depends_on.redis.condition: service_healthy` entry. Assert no `6379:6379` host exposure is introduced.

- [ ] **Step 2: Run the static test to verify it fails**

Run: `go test ./scripts -run 'TestComposeIncludesRedis' -count=1`

Expected: FAIL because Compose has no Redis service yet.

- [ ] **Step 3: Add the Redis service and health dependency**

Add:

```yaml
  redis:
    image: redis:7.4-alpine
    command: ["redis-server", "--appendonly", "yes"]
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 20
```

Set the API environment and dependency condition, preserve all existing MySQL/Jaeger/Prometheus/Alertmanager services and volumes, and append `redis_data` to the volume list. Do not add a host Redis port because the API is the intended local consumer.

- [ ] **Step 4: Run Compose and static checks**

Run: `docker compose config --quiet && go test ./scripts -run 'TestComposeIncludesRedis' -count=1`

Expected: both commands pass.

### Task 5: Add two-instance Redis smoke validation

**Files:**
- Create: `scripts/redis-rate-limit-smoke.sh`
- Modify: `scripts/scripts_test.go`
- Modify: `docs/stage8-performance-baseline.md`

**Interfaces:**
- Script accepts optional `REDIS_SMOKE_LIMIT` (default `2`) and `REDIS_SMOKE_PORT` (default `8889`).
- Script starts only local Compose services and one explicitly named temporary secondary API container; cleanup removes that container only.

- [ ] **Step 1: Write the shell script contract test**

Assert the script contains `REDIS_SMOKE_LIMIT`, `REDIS_ENVIRONMENT`, `REDIS_ADDR=redis:6379`, `docker rm -f`, two API URLs and an expectation for `429`. Assert it does not contain `docker compose down -v`, `FLUSHDB`, `DROP DATABASE`, or `user_order_api_test`/`user_order_api` deletion commands.

- [ ] **Step 2: Run the static test to verify it fails**

Run: `go test ./scripts -run 'TestRedisRateLimitSmokeScript' -count=1`

Expected: FAIL because the script does not exist.

- [ ] **Step 3: Implement the non-destructive two-instance smoke script**

Use `set -Eeuo pipefail`, create a unique `REDIS_ENVIRONMENT` suffix from the current timestamp and shell PID, start `mysql jaeger redis api` with `REDIS_SMOKE_LIMIT` and the unique environment, wait for primary `/healthz`, obtain the built API image ID, and run a secondary container on the Compose network with the same MySQL DSN, JWT key, Redis address and environment. Send two total `GET /api/v1/health` requests split between ports `8888` and the secondary port, then send a third request and require HTTP 429. Print response bodies on failure. A trap must remove only the named secondary container. The unique environment avoids touching counters from unrelated local tests without deleting Redis data.

- [ ] **Step 4: Document and run the script**

Document:

```bash
bash scripts/redis-rate-limit-smoke.sh
```

Explain that success proves the two instances share one Redis window; it does not test business writes. Run `bash -n scripts/redis-rate-limit-smoke.sh`, the static test, then the script while preserving existing MySQL volumes. Expected: first two requests succeed across different instances and the third returns 429.

### Task 6: Add failure-mode tests and documentation

**Files:**
- Modify: `internal/platform/security/counter_store_test.go`
- Modify: `internal/platform/security/ratelimit_test.go`
- Modify: `docs/architecture.md`
- Modify: `docs/implementation-roadmap.md`
- Modify: `README.md`

**Interfaces:**
- Documents the difference between no Redis (memory mode), configured Redis (shared mode), startup failure, and runtime 503.

- [ ] **Step 1: Add deterministic failure-mode tests**

Test that a fake store error causes 503 without invoking the downstream handler, that the response code is exactly `RATE_LIMIT_BACKEND_UNAVAILABLE`, and that the error detail is not present. Test that a memory store resets after one minute using a controlled clock. Keep all existing rate-limit and trusted-proxy tests.

- [ ] **Step 2: Run the focused failure tests**

Run: `go test ./internal/platform/security -run 'Test.*(Backend|Memory|RateLimiter)' -count=1`

Expected: PASS.

- [ ] **Step 3: Update project documentation**

In `README.md`, add the Redis-backed Compose startup behavior and the host-direct fallback. In `docs/architecture.md`, add Redis beside the API and explain that MySQL remains the source of truth. In `docs/implementation-roadmap.md`, mark the Redis distributed-rate-limit slice as the current Stage 8 deliverable and list the remaining Stage 8 work separately if any. In `docs/stage8-performance-baseline.md`, add the smoke script, expected 429 behavior, `REDIS_ADDR` troubleshooting, and the warning that `docker compose down -v` is forbidden for normal validation.

- [ ] **Step 4: Run documentation and static checks**

Run: `go test ./scripts -count=1 && git diff --check && rg -n 'PLACEHOLDER' docs README.md scripts internal cmd || true`

Expected: tests pass, diff check is clean, and no new placeholder text is found.

### Task 7: Full verification checkpoint

**Files:**
- Verify all files changed by Tasks 1–6; no new source changes unless a verification failure identifies a concrete defect.

- [ ] **Step 1: Run the complete Go test suite**

Run: `go test ./...`

Expected: PASS; MySQL integration tests use only the configured test database and may skip when `MYSQL_TEST_DSN` is unset.

- [ ] **Step 2: Run static analysis and race detection**

Run: `go vet ./... && go test -race ./...`

Expected: both PASS.

- [ ] **Step 3: Validate Compose and local service health**

Run: `docker compose config --quiet && docker compose up --build -d && docker compose ps`

Expected: `redis`, `mysql`, `api`, `prometheus`, `jaeger` and `alertmanager` are running/healthy as applicable; `curl -fsS http://localhost:8888/healthz` succeeds; no volumes are deleted.

- [ ] **Step 4: Run Redis-specific acceptance**

Run: `bash scripts/redis-rate-limit-smoke.sh`

Expected: two instances share the configured low limit and the third request receives 429. If the script fails, inspect `docker compose logs api redis` and fix the concrete issue before claiming completion.

- [ ] **Step 5: Leave changes uncommitted for user review**

Run: `git status --short && git diff --stat`

Expected: all Redis implementation, tests, Compose and docs changes are visible for the user to review; do not run any Git write command.

## Self-Review Checklist

- Spec coverage: Tasks 1–2 cover the CounterStore interface, Lua algorithm, key format and 429/503 behavior; Task 3 covers configuration and startup/runtime ownership; Task 4 covers Compose; Task 5 covers two-instance acceptance; Task 6 covers documentation and deterministic failure tests; Task 7 covers every required verification command.
- Placeholder scan: the plan contains no placeholder instructions.
- Type consistency: `CounterStore`, `NewRedisCounterStore`, `NewRateLimiterWithStore`, `app.Config.RateLimitStore`, and `app.Dependencies.RateLimitStore` are introduced before later tasks consume them.
- Safety: no task drops databases, flushes Redis, removes volumes, or commits to `main`.
