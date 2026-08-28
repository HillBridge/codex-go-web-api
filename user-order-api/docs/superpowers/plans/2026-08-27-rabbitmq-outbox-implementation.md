# RabbitMQ + MySQL Outbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为现有用户、订单和认证业务增加可靠的 Outbox 事件投递链路，并通过 RabbitMQ 异步处理审计事件，同时保持现有同步 HTTP API 语义。

**Architecture:** 在 MySQL 事务中同时写入业务数据和 `outbox_events`，Publisher 批量领取并通过 RabbitMQ Publisher Confirm 发布，Consumer 以手动 ACK、Inbox 幂等和 DLQ 处理审计事件。`RABBITMQ_URL` 为空时保留当前 `AsyncLogger`；配置后由 `cmd/api` 创建后台 Publisher/Consumer，API Handler 不直接依赖 RabbitMQ。

**Tech Stack:** Go 1.25.3、`github.com/rabbitmq/amqp091-go`、MySQL 8.4、RabbitMQ 4.x Management Alpine、Docker Compose、现有 Prometheus/OpenTelemetry/slog。

**Spec:** `docs/superpowers/specs/2026-08-27-rabbitmq-outbox-design.md`

## Global Constraints

- 未配置 `RABBITMQ_URL` 时保持现有内存审计和宿主机启动行为。
- 配置 `RABBITMQ_URL` 后启动阶段必须连接、声明拓扑并确认成功；失败时 API 不监听端口。
- 业务数据和 Outbox 事件必须在同一个 MySQL 事务中提交，不能先提交业务数据再补写事件。
- RabbitMQ 只负责异步传递；用户、订单、会话主数据仍由 MySQL 保存。
- Publisher 使用持久化消息和 Publisher Confirm；Consumer 使用手动 ACK、重试和死信。
- 事件 payload 与日志不得包含密码、Token、Cookie、完整请求体或敏感认证信息。
- 本地集成测试只使用 `user_order_api_test`；不得删除或重置 `user_order_api`，不得执行 `docker compose down -v`。
- 不执行 `git add`、`git commit` 或 `git push`，保留改动供用户审核。

## File Map

- Create: `internal/platform/outbox/event.go` — 事件模型、状态和事件构造约束。
- Create: `internal/platform/outbox/repository.go` — Outbox/Inbox SQL 接口与 MySQL 实现。
- Create: `internal/platform/outbox/worker.go` — Publisher 轮询、领取、重试和指标边界。
- Create: `internal/platform/messaging/rabbitmq.go` — RabbitMQ 连接、拓扑、发布确认和消费通道。
- Create: `internal/platform/audit/consumer.go` — RabbitMQ 审计 Consumer 和脱敏日志处理。
- Create: `internal/platform/database/migrations/0006_outbox_events.sql`
- Create: `internal/platform/database/migrations/0007_inbox_events.sql`
- Modify: `internal/user/mysql_repository.go`, `internal/user/service.go` — 用户创建事务化事件。
- Modify: `internal/order/mysql_repository.go`, `internal/order/service.go` — 订单创建与状态变更事务化事件。
- Modify: `internal/auth/mysql_repository.go`, `internal/auth/service.go` — 认证会话相关事务化事件。
- Modify: `internal/app/application.go`, `internal/app/composition.go` — 注入 Outbox、Publisher、Consumer 和降级审计实现。
- Modify: `cmd/api/config.go`, `cmd/api/config_test.go`, `cmd/api/main.go` — MQ 配置和 Worker 生命周期。
- Modify: `compose.yaml` — RabbitMQ 服务、凭据、健康检查和持久化卷。
- Create: `scripts/rabbitmq-outbox-smoke.sh` — 本地可靠投递、重启恢复和消费幂等验收。
- Modify: `scripts/scripts_test.go` — Compose、脚本安全约束和配置静态测试。
- Modify: `internal/platform/observability/metrics.go` — Outbox/MQ 指标。
- Modify: `README.md`, `docs/architecture.md`, `docs/implementation-roadmap.md`, `docs/stage8-performance-baseline.md` — 运行、故障和阶段文档。

---

### Task 1: Add migrations and event model

**Files:**
- Create: `internal/platform/database/migrations/0006_outbox_events.sql`
- Create: `internal/platform/database/migrations/0007_inbox_events.sql`
- Create: `internal/platform/outbox/event.go`
- Test: `internal/platform/outbox/event_test.go`

**Interfaces:**
- `outbox.Event{EventID, EventType, AggregateType, AggregateID, Payload, OccurredAt}`.
- `outbox.StatusPending`, `StatusPublishing`, `StatusPublished`, `StatusDead`.
- `Event.Validate()` rejects an empty ID/type, empty payload, or sensitive JSON keys.

- [ ] **Step 1: Write failing event validation tests**

Cover valid `order.created`, missing event ID, missing event type, empty payload, and payload containing `password`, `accessToken`, `refreshToken`, or `cookie`.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/platform/outbox -run TestEvent -count=1`

Expected: FAIL because the package and types do not exist.

- [ ] **Step 3: Implement the model and migrations**

Add the exact `outbox_events` and `inbox_events` schemas from the approved spec, including unique `event_id`, status/availability indexes, lock expiry, attempts, error, timestamps, and Inbox primary key `(consumer_name,event_id)`. Use UTC timestamps and JSON payload storage.

- [ ] **Step 4: Run focused tests and migration checks**

Run: `gofmt -w internal/platform/outbox && go test ./internal/platform/outbox -run TestEvent -count=1 && GOCACHE=/tmp/user-order-api-migration-cache go test ./internal/platform/database -run TestApplyMigrations -count=1`

Expected: PASS when the test database is configured; otherwise the migration integration test may skip according to existing test conventions.

### Task 2: Implement Outbox/Inbox repositories

**Files:**
- Create: `internal/platform/outbox/repository.go`
- Create: `internal/platform/outbox/repository_test.go`

**Interfaces:**
- `type Repository interface { AppendTx(context.Context, *sql.Tx, Event) error; ClaimBatch(context.Context, string, time.Time, int, time.Duration) ([]Event,error); MarkPublished(context.Context, string, []string) error; MarkRetry(context.Context, string, error, time.Time, int, bool) error }`.
- `type Inbox interface { Claim(context.Context, string, string, time.Time, time.Duration) (bool,error); MarkProcessed(context.Context,string,string,time.Time) error; MarkFailed(context.Context,string,string,error,time.Time) error }`.
- `NewMySQLRepository(*sql.DB) *MySQLRepository`.

- [ ] **Step 1: Write repository tests against `user_order_api_test`**

Test appending an event in a transaction, rolling back and confirming no row, committing and confirming one row, claiming only available pending rows, reclaiming expired publishing rows, marking published, and Inbox duplicate/expired-claim behavior. Every test must use the existing test database helper and clean only its generated event IDs.

- [ ] **Step 2: Run repository tests to verify the initial failures**

Run: `MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' go test ./internal/platform/outbox -run 'Test(MySQL|Outbox|Inbox)' -count=1`

Expected: FAIL until the repository and migrations are implemented.

- [ ] **Step 3: Implement short-transaction claiming and idempotent Inbox operations**

Use `FOR UPDATE SKIP LOCKED` to select pending or expired publishing rows, update their lock token/expiry, commit the claim transaction, and publish only after the transaction ends. `MarkPublished` must require the lock token. `MarkRetry` must set `available_at`, clear lock fields, save `last_error`, and choose `pending` or `dead` based on the caller’s final-attempt flag. Inbox claim must atomically insert or reclaim expired `processing`; `processed` rows return `false` without executing the handler.

- [ ] **Step 4: Run repository tests to verify they pass**

Run the same `MYSQL_TEST_DSN=... go test ./internal/platform/outbox -run 'Test(MySQL|Outbox|Inbox)' -count=1` command.

Expected: PASS without touching `user_order_api`.

### Task 3: Integrate transactional events into user, order and auth writes

**Files:**
- Modify: `internal/user/mysql_repository.go`, `internal/user/service.go`
- Modify: `internal/order/mysql_repository.go`, `internal/order/service.go`
- Modify: `internal/auth/mysql_repository.go`, `internal/auth/service.go`
- Modify: `internal/platform/outbox/event.go`
- Test: `internal/user/service_test.go`, `internal/order/service_test.go`, `internal/auth/service_test.go`, `internal/app/http_test.go`

**Interfaces:**
- Add transaction-aware optional repository methods that accept an event factory, for example `CreateWithEvent(ctx,input,func(User) outbox.Event)` and `TransitionWithEvent(ctx,id,target,func(Order) outbox.Event)`; existing read and memory interfaces remain source-compatible.
- Add auth transaction methods covering registration/session creation, login session creation, refresh rotation and logout revoke; each method writes its event in the same transaction as the session mutation.
- Services construct only these event types and sanitized fields: `auth.*` with user/session IDs, `user.created` with user ID, and `order.*` with order/user IDs.

- [ ] **Step 1: Add failing transaction boundary tests**

Use SQL mocks or the test database to prove an Outbox write failure rolls back the corresponding user/order/session mutation. Verify order replay does not create a second event, and unchanged order transitions do not create events. Extend existing authentication audit tests to assert all eight event types are produced through the event writer.

- [ ] **Step 2: Run focused tests and verify failures**

Run: `go test ./internal/user ./internal/order ./internal/auth ./internal/app -run 'Test.*(Event|Outbox|Transaction|Audit)' -count=1`

Expected: FAIL because transaction-aware methods and event writer injection are absent.

- [ ] **Step 3: Implement the minimal transactional repository paths**

MySQL repositories begin a transaction, write the business row, build the event after the generated ID is known, append Outbox through `AppendTx`, and commit once. On any Outbox or commit error, rollback and return the existing service error. Memory repositories continue to execute synchronously and use the current audit recorder in tests. Keep idempotency and conditional order transitions unchanged.

- [ ] **Step 4: Run focused tests and the MySQL HTTP flow**

Run: `MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' go test ./internal/user ./internal/order ./internal/auth ./internal/app -run 'Test.*(Event|Outbox|Transaction|Audit|MySQL)' -count=1`

Expected: PASS; only the test database is modified and generated rows are cleaned by tests.

### Task 4: Add RabbitMQ topology and connection abstraction

**Files:**
- Create: `internal/platform/messaging/rabbitmq.go`
- Create: `internal/platform/messaging/rabbitmq_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- `type Broker interface { Publish(context.Context, string, []byte) error; Consume(context.Context) (<-chan Delivery,error); Close() error }`.
- `NewBroker(ctx context.Context, url, exchange, queue string, prefetch int) (*RabbitBroker,error)` connects, declares durable exchange/queues/DLX/retry topology and waits for a successful connection.
- `Delivery` exposes `EventID`, `Body`, `Attempt`, `Ack()`, `Retry()`, and `DeadLetter()`.

- [ ] **Step 1: Write failing topology and serialization tests**

Test event JSON contains event ID/type/aggregate/payload but no sensitive fields; test invalid URL fails connection setup; add a broker contract test that can run when `RABBITMQ_TEST_URL` is set and skips otherwise.

- [ ] **Step 2: Run focused tests to verify failures**

Run: `go test ./internal/platform/messaging -run 'Test(Broker|Event)' -count=1`

Expected: FAIL because the broker package and dependency are absent.

- [ ] **Step 3: Implement durable RabbitMQ setup**

Use `github.com/rabbitmq/amqp091-go`, durable topic exchange, durable audit/retry/DLQ queues, persistent delivery mode, manual ACK, bounded prefetch, and publisher confirms. Retry messages must carry the event ID and attempt count; the broker must not log credentials.

- [ ] **Step 4: Run broker tests with local RabbitMQ when available**

Run: `go test ./internal/platform/messaging -run 'Test(Broker|Event)' -count=1`.

Expected: unit tests PASS; integration test PASS when `RABBITMQ_TEST_URL` points to local Compose RabbitMQ.

### Task 5: Implement Publisher and Consumer workers

**Files:**
- Create: `internal/platform/outbox/worker.go`
- Create: `internal/platform/outbox/worker_test.go`
- Create: `internal/platform/audit/consumer.go`
- Create: `internal/platform/audit/consumer_test.go`
- Modify: `internal/platform/observability/metrics.go`

**Interfaces:**
- `Publisher.Run(context.Context) error` polls and publishes until cancellation.
- `Consumer.Run(context.Context) error` receives deliveries and processes them until cancellation.
- Both workers expose bounded counters used by `observability.Metrics`.

- [ ] **Step 1: Write failing worker tests**

Use fake Outbox/Inbox/Broker implementations to test: batch size, lock token use, successful confirm marks published, broker failure schedules retry, max attempts marks dead, processed Inbox event is ACKed without duplicate handling, handler failure retries, and retry exhaustion dead-letters.

- [ ] **Step 2: Run worker tests to verify failures**

Run: `go test ./internal/platform/outbox ./internal/platform/audit -run 'Test(Publisher|Consumer)' -count=1`

Expected: FAIL because workers and metrics are absent.

- [ ] **Step 3: Implement worker loops**

Publisher uses a `time.Ticker`, batch size and backoff configuration; it claims rows in short transactions, publishes with confirm, and marks state. Consumer claims Inbox state before invoking a handler, records processed/failed status, ACKs only after success, and uses broker retry/DLQ operations. Cancellation closes channels and lets in-flight handlers finish within the application shutdown timeout.

- [ ] **Step 4: Run worker tests and metrics tests**

Run: `go test ./internal/platform/outbox ./internal/platform/audit ./internal/platform/observability -run 'Test(Publisher|Consumer|Registry)' -count=1`

Expected: PASS.

### Task 6: Wire configuration, application lifecycle and Compose

**Files:**
- Modify: `cmd/api/config.go`, `cmd/api/config_test.go`, `cmd/api/main.go`
- Modify: `internal/app/application.go`, `internal/app/composition.go`
- Modify: `compose.yaml`
- Modify: `scripts/scripts_test.go`

**Interfaces:**
- `serverConfig` gains `RabbitMQURL`, `RabbitMQExchange`, `RabbitMQAuditQueue`, `OutboxPollInterval`, `OutboxBatchSize`, `OutboxMaxAttempts`, `ConsumerPrefetch`, `ConsumerMaxRetries`.
- `app.Config` and `app.Dependencies` gain Outbox writer/worker dependencies without importing the RabbitMQ client.

- [ ] **Step 1: Write failing config and Compose tests**

Assert defaults, duration/integer validation, trimmed `RABBITMQ_URL`, Compose `rabbitmq:4-management-alpine`, port `5672` internal/`15672` local UI, `rabbitmq-diagnostics -q ping` healthcheck, API `depends_on.rabbitmq.service_healthy`, and a persistent `rabbitmq_data` volume. Assert no destructive volume command appears.

- [ ] **Step 2: Run focused tests to verify failures**

Run: `go test ./cmd/api ./scripts -run 'Test(LoadConfig|ComposeIncludesRabbitMQ)' -count=1`

Expected: FAIL because configuration and Compose service are absent.

- [ ] **Step 3: Implement startup and graceful shutdown wiring**

Parse and validate all MQ settings. When `RabbitMQURL` is empty, construct the existing AsyncLogger-only path. When configured, create the broker with a ten-second startup context, construct Publisher/Consumer, start them after migrations and before serving, and fail startup if broker setup fails. On shutdown cancel workers, wait for them, close RabbitMQ, drain audit logging, then close MySQL. `/readyz` must include RabbitMQ readiness only when MQ is enabled.

- [ ] **Step 4: Add Compose RabbitMQ and run static checks**

Add the durable RabbitMQ service, healthcheck, credentials from non-production local defaults, API environment interpolation, dependency condition and volume. Run `docker compose config --quiet && go test ./cmd/api ./scripts -run 'Test(LoadConfig|ComposeIncludesRabbitMQ)' -count=1`.

Expected: PASS.

### Task 7: Add end-to-end smoke validation and documentation

**Files:**
- Create: `scripts/rabbitmq-outbox-smoke.sh`
- Modify: `scripts/scripts_test.go`
- Modify: `README.md`, `docs/architecture.md`, `docs/implementation-roadmap.md`, `docs/stage8-performance-baseline.md`

**Interfaces:**
- Script accepts `RABBITMQ_SMOKE_EMAIL` and `RABBITMQ_SMOKE_PASSWORD` only when an existing test account is required; event verification itself must not create business data in the real database.

- [ ] **Step 1: Write script contract tests**

Assert the script starts `mysql redis rabbitmq jaeger api`, waits for RabbitMQ health, calls a read-only API health endpoint plus a test-only event inspection command, checks `published`/`processed` state and retry behavior, and cleans only explicitly named temporary containers. Forbid `docker compose down -v`, `DROP DATABASE`, `FLUSHDB`, and broad volume deletion.

- [ ] **Step 2: Run static script tests to verify failure**

Run: `go test ./scripts -run 'TestRabbitMQOutboxSmokeScript' -count=1`

Expected: FAIL because the smoke script does not exist.

- [ ] **Step 3: Implement safe local end-to-end verification**

Use a unique smoke environment and an existing test account when authentication is needed. Trigger one user/order event only in `user_order_api_test`, query Outbox status using a read-only SQL query, wait for RabbitMQ Consumer processing, verify Inbox processed state, stop/restart API without removing volumes, and confirm the event is not lost or duplicated. Print `event_id`, `event_type`, Outbox status and Inbox status on success; never print payload secrets.

- [ ] **Step 4: Document operation and troubleshooting**

Document `docker compose up --build -d`, RabbitMQ Management UI at `http://localhost:15672`, Outbox status queries, retry/DLQ behavior, Redis and RabbitMQ dependency differences, and safe stop commands. Update Stage 8 status to “MQ reliable audit delivery completed” while leaving future external notification consumers and microservice decomposition as separate decisions.

### Task 8: Full verification checkpoint

**Files:**
- Verify all files from Tasks 1–7; no unrelated refactoring.

- [ ] **Step 1: Run all Go tests and static analysis**

Run: `GOCACHE=/tmp/user-order-api-mq-cache go test ./... && GOCACHE=/tmp/user-order-api-mq-vet go vet ./... && GOCACHE=/tmp/user-order-api-mq-race go test -race ./...`

Expected: all commands PASS; MySQL integration tests use only `user_order_api_test`.

- [ ] **Step 2: Validate Compose and service health**

Run: `docker compose config --quiet && docker compose up --build -d && docker compose ps`.

Expected: API, MySQL, Redis, RabbitMQ, Prometheus, Jaeger and Alertmanager are running/healthy as applicable; API `/healthz` and `/readyz` return 200.

- [ ] **Step 3: Run MQ smoke validation**

Run: `bash scripts/rabbitmq-outbox-smoke.sh`.

Expected: event reaches `published` and `processed`, transient failure is retried, duplicate delivery is idempotent, and no real database data is deleted.

- [ ] **Step 4: Leave changes uncommitted for review**

Run: `git status --short && git diff --stat && git diff --check`.

Expected: all implementation, tests and documentation changes are visible for user review; no Git write command has been executed.

## Self-Review Checklist

- Spec coverage: Tasks 1–2 cover schemas and storage; Task 3 covers all eight event types and atomic business writes; Task 4 covers RabbitMQ topology; Task 5 covers Publisher/Consumer reliability; Task 6 covers configuration/lifecycle/Compose; Task 7 covers safe end-to-end validation and docs; Task 8 covers every required verification command.
- Placeholder scan: the plan contains concrete interfaces, commands, failure expectations and no incomplete implementation instructions.
- Type consistency: `outbox.Event`, `outbox.Repository`, `outbox.Inbox`, `messaging.Broker`, `Publisher`, `Consumer`, and the application configuration fields are introduced before later tasks consume them.
- Safety: no task removes databases, clears Redis/RabbitMQ globally, deletes volumes, or commits to `main`.
