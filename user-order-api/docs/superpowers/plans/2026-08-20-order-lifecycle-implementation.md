# 订单生命周期与幂等性 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为订单提供并发安全的支付、取消状态流转，以及创建订单的幂等性保障。

**Architecture:** `order.Service` 保持业务规则、HTTP 错误码和审计事件的边界；`order.Repository` 负责内存与 MySQL 的一致行为。MySQL 以唯一索引保证幂等键并发安全，以条件更新保证订单只能从 `pending` 流转一次。

**Tech Stack:** Go、标准库 `net/http`、`database/sql`、MySQL 8、`go-sql-driver/mysql`、嵌入式 SQL 迁移、OpenAPI 3.0、Postman Collection。

**Spec:** `docs/superpowers/specs/2026-08-20-order-lifecycle-design.md`

## Global Constraints

- 所有 API 保持 `/api/v1` 路由前缀，金额为人民币分的正整数，时间为 UTC RFC 3339。
- 创建订单的 `Idempotency-Key` 是可选请求头；去除两端空白后必须非空且最多 255 字符。
- 无幂等键时保留现有创建行为；同键同请求返回已有订单 `200`；同键不同请求返回 `409 IDEMPOTENCY_KEY_CONFLICT`。
- 只允许 `pending -> paid` 和 `pending -> cancelled`；重复目标操作返回当前订单 `200`，跨终态操作返回 `409 INVALID_ORDER_STATE`。
- 仅实际创建或实际状态改变写入审计：`order.created`、`order.paid`、`order.cancelled`。
- MySQL 集成测试只能使用 `MYSQL_TEST_DSN` 指向 `user_order_api_test`。

---

### Task 1: 领域模型、内存仓储与服务状态机

**Files:**
- Modify: `internal/order/model.go`
- Modify: `internal/order/repository.go`
- Modify: `internal/order/service.go`
- Modify: `internal/order/repository_test.go`
- Create: `internal/order/service_test.go`

**Interfaces:**
- Produces: `CreateOrderRequest{UserID int64, Amount int64, IdempotencyKey string}`。
- Produces: `Repository.Create(ctx, input) (order Order, replayed bool, err error)` 和 `Repository.Transition(ctx, id int64, target Status) (order Order, changed bool, err error)`。
- Produces: `Service.Create(ctx, input) (Order, replayed bool, error)`、`Service.Pay(ctx, id)`、`Service.Cancel(ctx, id)`。

- [x] **Step 1: Write the failing unit tests**

```go
func TestMemoryRepositoryCreateReplaysMatchingIdempotencyKey(t *testing.T) {
    repo := NewMemoryRepository()
    first, replayed, err := repo.Create(context.Background(), CreateOrderRequest{UserID: 1, Amount: 2599, IdempotencyKey: "create-1"})
    if err != nil || replayed { t.Fatalf("first create = (%+v, %t, %v)", first, replayed, err) }
    second, replayed, err := repo.Create(context.Background(), CreateOrderRequest{UserID: 1, Amount: 2599, IdempotencyKey: "create-1"})
    if err != nil || !replayed || second.ID != first.ID { t.Fatalf("replay = (%+v, %t, %v)", second, replayed, err) }
}
```

以及覆盖 `pending -> paid`、重复支付和 `paid -> cancelled` 冲突的服务层测试。

- [x] **Step 2: Run test to verify it fails**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/order -run 'TestMemoryRepositoryCreateReplaysMatchingIdempotencyKey|TestServiceRejectsTransitionAcrossTerminalStates' -count=1`

Expected: FAIL，因为新的仓储返回值、状态流转方法和服务方法尚不存在。

- [x] **Step 3: Write minimal implementation**

```go
var (
    ErrIdempotencyConflict = errors.New("idempotency key conflicts with existing order")
    ErrInvalidState        = errors.New("invalid order state transition")
)

func (r *MemoryRepository) Transition(ctx context.Context, id int64, target Status) (Order, bool, error) {
    // pending 时更新并返回 changed=true；已是 target 返回 changed=false；其他终态返回 ErrInvalidState。
}
```

服务层校验幂等键、映射冲突为 `409`，并且只在 `replayed == false` 或 `changed == true` 时记录对应审计事件。

- [x] **Step 4: Run tests to verify they pass**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/order -count=1`

Expected: PASS，覆盖内存幂等、目标状态重复调用、跨终态冲突和审计只记录实际变化。

### Task 2: MySQL 迁移与并发安全仓储

**Files:**
- Create: `internal/platform/database/migrations/0003_orders_idempotency_key.sql`
- Modify: `internal/order/mysql_repository.go`
- Modify: `internal/order/mysql_repository_integration_test.go`
- Modify: `internal/platform/database/database_integration_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Repository` 方法和领域错误。
- Produces: MySQL 对 `idempotency_key` 的唯一约束；MySQL 仓储与内存仓储相同的创建和状态流转语义。

- [x] **Step 1: Write the failing MySQL integration tests**

```go
func TestMySQLRepositoryReturnsOneOrderForConcurrentMatchingIdempotencyKey(t *testing.T) {
    // 对同一个 key 并发执行两次 Create；断言两个结果 ID 相同，并查询到仅一条订单。
}
```

再覆盖参数冲突和 `pending -> paid -> cancelled` 的持久化结果。

- [x] **Step 2: Run test to verify it fails**

Run: `MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/order -run MySQL -count=1`

Expected: FAIL，因为迁移、幂等列和状态转换尚未实现。

- [x] **Step 3: Write minimal implementation**

```sql
ALTER TABLE orders
    ADD COLUMN idempotency_key VARCHAR(255) NULL,
    ADD CONSTRAINT uq_orders_idempotency_key UNIQUE (idempotency_key);
```

有键时插入，唯一键冲突后按键查询并比较 `user_id` 与 `amount`。状态转换使用 `UPDATE orders SET status = ? WHERE id = ? AND status = 'pending'`，更新零行时读取订单区分不存在、重复目标和非法转换。

- [x] **Step 4: Run tests to verify they pass**

Run: `MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/order ./internal/platform/database -count=1`

Expected: PASS，包含迁移、唯一键、并发幂等和状态持久化。

### Task 3: HTTP 路由与契约验证

**Files:**
- Modify: `internal/order/handler.go`
- Modify: `cmd/api/server_test.go`

**Interfaces:**
- Consumes: `Service.Create` 的 `replayed` 结果，以及 `Service.Pay`、`Service.Cancel`。
- Produces: `POST /orders/{id}/pay`、`POST /orders/{id}/cancel`，以及有幂等重放时 `POST /orders` 返回 `200`。

- [x] **Step 1: Write the failing HTTP contract tests**

```go
func TestOrderCreateReplaysIdempotencyKey(t *testing.T) {
    // 两次带相同 Idempotency-Key 的 POST /api/v1/orders：首次 201，第二次 200，订单 ID 相同。
}
```

再覆盖支付、重复支付、跨终态取消以及它们的错误码。

- [x] **Step 2: Run test to verify it fails**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api -run 'TestOrderCreateReplaysIdempotencyKey|TestOrderLifecycleHTTPContract' -count=1`

Expected: FAIL，因为动作路由和 200 重放响应尚未实现。

- [x] **Step 3: Write minimal implementation**

`POST /orders` 读取并裁剪 `Idempotency-Key`；重放时写入 `200`，新建时写入 `201`。`/orders/{id}/pay` 和 `/orders/{id}/cancel` 只接受 POST，解析路径 ID 并返回更新后的订单；其他方法返回正确 `Allow` 头的 JSON 405。

- [x] **Step 4: Run tests to verify they pass**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api -count=1`

Expected: PASS，覆盖创建、幂等重放、支付、取消和错误码。

### Task 4: API 文档、Postman 与完整回归

**Files:**
- Modify: `docs/openapi.yaml`
- Modify: `docs/postman/user-order-api.postman_collection.json`
- Modify: `README.md`
- Modify: `docs/architecture.md`

**Interfaces:**
- Consumes: Tasks 1–3 最终 API 契约。
- Produces: 前端和测试人员可直接使用的接口描述及请求示例。

- [x] **Step 1: 更新 OpenAPI 和 Postman**

为创建接口声明 `Idempotency-Key` 头、`200/201/409` 响应；增加支付和取消路径及状态冲突响应。Postman Collection 增加订单 ID、幂等键、支付/取消请求。

- [x] **Step 2: 更新项目说明与架构说明**

在 README 写明状态机、金额单位和本地验证方式；在架构说明中补充幂等键、条件更新和审计事件的责任边界。

- [x] **Step 3: 格式化并执行完整验证**

Run:

```bash
gofmt -w internal/order cmd/api
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test ./... -count=1
GOCACHE=/private/tmp/user-order-api-go-cache go vet ./...
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test -race ./...
git diff --check
```

Expected: 所有命令以 0 退出；数据库测试只影响 `user_order_api_test`。
