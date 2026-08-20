# 订单生命周期与幂等性设计

## 目标

让订单具备明确、可并发安全执行的生命周期，并保证客户端重试创建订单时不会产生重复订单。

## 范围

- 支持订单状态：`pending`、`paid`、`cancelled`。
- 支持确认付款与取消订单。
- 支持创建订单的幂等键。
- 更新 MySQL 迁移、OpenAPI、Postman Collection、README 与架构说明。

本阶段不接入支付网关、库存、退款、消息队列或多币种。

## 状态机

新订单固定为 `pending`，允许的转换只有：

```text
pending ──pay──> paid
pending ──cancel──> cancelled
```

- `paid` 和 `cancelled` 是终态，不能互相转换。
- 对已经处于目标状态的请求，返回当前订单与 `200 OK`，不产生第二次状态修改或审计事件。
- 对终态订单执行另一种转换，返回 `409 Conflict`，错误码 `INVALID_ORDER_STATE`。
- 不存在的订单返回 `404 Not Found`，错误码 `ORDER_NOT_FOUND`。

## HTTP 契约

所有路径使用现有的 `/api/v1` 前缀。

| 用途 | 方法与路径 | 成功结果 |
| --- | --- | --- |
| 创建订单 | `POST /orders` | 新建时 `201`；幂等重放时 `200` |
| 确认支付 | `POST /orders/{id}/pay` | `200`，返回订单 |
| 取消订单 | `POST /orders/{id}/cancel` | `200`，返回订单 |

创建订单的 `Idempotency-Key` 请求头是可选的非空字符串，去除首尾空白后长度必须不超过 255 字符。

- 未提供该头：保留现有创建行为。
- 提供新 key：创建订单，返回 `201`。
- 提供已存在 key 且 `userId`、`amount` 相同：返回首次创建的订单，HTTP 状态 `200`。
- 提供已存在 key 但请求内容不同：返回 `409` 与 `IDEMPOTENCY_KEY_CONFLICT`。

金额继续使用人民币分的正整数，时间继续使用 UTC RFC 3339。错误响应继续使用 `{ "code": "...", "error": "..." }`。

## 数据模型与并发

新增迁移 `0003_orders_idempotency_key.sql`，在 `orders` 表上增加可空、唯一的 `idempotency_key VARCHAR(255)`。已有订单的键为 `NULL`。

MySQL 仓储创建订单时：

1. 无幂等键时直接插入。
2. 有幂等键时尝试插入。
3. 唯一键冲突时按 key 查询已有订单，并比较 `user_id` 与 `amount`。
4. 内容一致时返回已有订单并标记为重放；不一致时返回领域冲突错误。

数据库唯一约束是并发安全的最终保障。状态转换使用单条带条件的更新：`UPDATE orders SET status = ? WHERE id = ? AND status = 'pending'`。受影响行数为零时读取当前订单，区分“不存在”“已经是目标状态”和“非法转换”。

## 领域边界

`order.Repository` 新增带幂等结果的创建方法和状态转换方法。Service 负责验证请求、将领域错误翻译为 HTTP 错误码，并仅在实际状态变化后写入审计事件：

- `order.created`
- `order.paid`
- `order.cancelled`

MemoryRepository 与 MySQLRepository 都实现同一行为；前者服务单元测试，后者服务生产。

## 验证

- 单元测试覆盖所有状态转换、重复转换和非法转换。
- 集成测试覆盖 MySQL 唯一键幂等、重复 key 的参数冲突、并发创建只产生一条订单，以及状态转换的持久化结果。
- HTTP 测试覆盖状态码、错误码和响应体。
- 最终运行 MySQL 集成测试、`go test ./...`、`go vet ./...`、`go test -race ./...` 与 API 手工验收。
