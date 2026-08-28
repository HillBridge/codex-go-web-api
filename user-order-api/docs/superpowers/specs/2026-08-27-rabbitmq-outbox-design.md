# 阶段 8：RabbitMQ + MySQL Outbox 可靠异步设计

## 目标

将当前进程内的最佳努力审计队列升级为可靠异步事件链路：业务数据和事件在同一个 MySQL 事务中落库，后台 Publisher 将 Outbox 事件投递到 RabbitMQ，Consumer 负责审计处理、确认、重试和死信。HTTP 接口仍保持同步业务语义，用户、订单、会话的事实来源仍然是 MySQL。

## 本阶段范围

覆盖当前代码已经产生的事件：

- `auth.registered`
- `auth.logged_in`
- `auth.refreshed`
- `auth.logged_out`
- `user.created`
- `order.created`
- `order.paid`
- `order.cancelled`

本阶段包含：

- `outbox_events` 和消费幂等表的数据库迁移；
- 业务写入与 Outbox 写入的事务边界；
- RabbitMQ 连接、拓扑、持久化消息和 Publisher Confirm；
- Outbox 批量领取、发布、重试和死信状态；
- Consumer 手动 ACK、消费幂等、失败重试和死信队列；
- API 进程内后台 Publisher/Consumer 的优雅启动和关闭；
- Prometheus 指标、日志字段和本地 Docker 验收。

本阶段不包含：

- 以 RabbitMQ 作为订单、用户或会话的主存储；
- 把同步创建订单改成异步命令接口；
- 真实短信、邮件、支付、库存等外部系统接入；
- Kafka、CDC、分布式事务、微服务拆分或云厂商专属 RabbitMQ 集群；
- 删除现有 `AsyncLogger` 的本地降级能力。

## 运行模式

| 配置 | 行为 |
| --- | --- |
| `RABBITMQ_URL` 为空 | 保留当前本地 `AsyncLogger`，适合未启动 MQ 的宿主机开发。业务接口不依赖 RabbitMQ。 |
| `RABBITMQ_URL` 已配置 | 启动时连接 RabbitMQ、声明拓扑并开启 Publisher/Consumer；连接失败时 API 启动失败。 |
| RabbitMQ 运行中断开 | Publisher 停止确认并保留 Outbox 待重试；Consumer 不 ACK 未完成消息；恢复连接后继续处理。 |
| Outbox 发布超过最大次数 | 标记 `dead` 并记录错误，Prometheus 告警；不删除原始事件。 |
| Consumer 处理失败 | 按退避策略重新投递；超过消费重试上限进入 RabbitMQ DLQ，同时保留消费失败记录。 |

Compose 默认设置：

```text
RABBITMQ_URL=amqp://app:app_password@rabbitmq:5672/
RABBITMQ_EXCHANGE=user-order-api.events
RABBITMQ_AUDIT_QUEUE=user-order-api.audit.v1
```

宿主机直接运行未设置 `RABBITMQ_URL` 时，不连接 MQ，仍可使用现有 MySQL + 内存审计模式。

## 数据模型

新增 `0006_outbox_events.sql`：

```sql
CREATE TABLE outbox_events (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    event_id CHAR(36) NOT NULL UNIQUE,
    event_type VARCHAR(100) NOT NULL,
    aggregate_type VARCHAR(50) NOT NULL,
    aggregate_id BIGINT NOT NULL,
    payload JSON NOT NULL,
    occurred_at DATETIME(6) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL,
    locked_until DATETIME(6) NULL,
    lock_token CHAR(64) NULL,
    published_at DATETIME(6) NULL,
    last_error VARCHAR(1000) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT chk_outbox_status CHECK (status IN ('pending', 'publishing', 'published', 'dead')),
    INDEX idx_outbox_publishable (status, available_at, id),
    INDEX idx_outbox_locked (status, locked_until, id)
);
```

新增 `0007_inbox_events.sql`：

```sql
CREATE TABLE inbox_events (
    consumer_name VARCHAR(100) NOT NULL,
    event_id CHAR(36) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'processing',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    locked_until DATETIME(6) NULL,
    processed_at DATETIME(6) NULL,
    last_error VARCHAR(1000) NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (consumer_name, event_id),
    INDEX idx_inbox_retry (consumer_name, status, locked_until)
);
```

`outbox_events.event_id` 是跨重试和跨队列的稳定幂等 ID；payload 只包含业务审计所需字段，不包含密码、Refresh Token 原文、Access Token、Cookie 或完整请求体。

## 业务事务边界

产生业务事件的写操作必须遵循：

```text
BEGIN
  写入 users / orders / sessions
  写入 outbox_events
COMMIT
```

事务提交前不得发布 RabbitMQ，事务失败不得产生可发布事件。现有读请求和健康检查不写 Outbox。

Repository 保持现有 Service 边界，不让 Handler 直接操作数据库表。MySQL Repository 增加事务化业务写入能力；内存 Repository 实现同一接口但只用于测试。Service 构造事件类型、聚合类型、聚合 ID 和脱敏 payload，Repository 在同一个 `sql.Tx` 中写业务表和 Outbox。事件写入失败时整个业务事务回滚，并将错误转换为现有统一错误响应。

## RabbitMQ 拓扑

```text
Exchange: user-order-api.events (topic, durable)
  ├─ Queue: user-order-api.audit.v1 (durable)
  │    └─ Consumer: audit worker, manual ACK
  ├─ Queue: user-order-api.audit.retry (durable, TTL + DLX)
  └─ Queue: user-order-api.audit.dlq (durable)
```

Publisher 使用持久化消息、`delivery_mode=2` 和 Publisher Confirm；只有收到 RabbitMQ Confirm 后才将 Outbox 标记为 `published`。发布前先以短事务领取一批 `pending` 或超时 `publishing` 事件，写入唯一的锁定标识和 `locked_until`，提交后再发送，避免长时间占用 MySQL 事务。

Publisher 每批最多领取 100 条，默认每 1 秒轮询；失败使用 1 秒、5 秒、30 秒、5 分钟的退避时间，超过 10 次转为 `dead`。多个 API 实例使用 `FOR UPDATE SKIP LOCKED` 和锁定期限，不能重复长期占用同一事件。

## Consumer 可靠性与幂等

Consumer 使用手动 ACK 和有限 prefetch。每次领取消息时，在 `inbox_events` 以 `(consumer_name, event_id)` 原子声明处理状态：

- `status=processed`：说明事件已处理，直接 ACK，不重复执行；
- 首次领取或 `processing` 已超过锁定期限：增加 `attempts` 并声明处理，执行审计处理，成功后更新为 `processed` 再 ACK；
- 处理失败：记录 `last_error`，不 ACK，进入退避重试；超过次数进入 DLQ；
- 进程在处理过程中崩溃：旧的 `processing` 锁过期后允许重新处理，保证事件不因“先写消费记录、后执行副作用”的顺序而丢失。

初始审计处理写结构化 stdout，因此进程在副作用完成、状态更新前崩溃时允许出现重复日志；对于未来需要严格幂等的持久化副作用，必须把副作用表写入和 `inbox_events.status=processed` 放在同一个 MySQL 事务中。

初始 Consumer 将事件写为结构化审计日志，并保留现有 stdout/slog 输出；后续通知、库存等消费者可复用同一事件契约，不在本阶段接入外部副作用。

## 组件边界

```text
internal/platform/outbox
  Event、Repository、Publisher、状态领取与重试

internal/platform/messaging
  RabbitMQ 连接、Exchange/Queue 声明、Publisher Confirm、Consumer ACK/DLQ

internal/platform/audit
  本地 AsyncLogger 降级实现，以及 MQ Consumer 的审计处理器

internal/app
  通过接口注入 Outbox 写入器和后台 Worker，不直接依赖 RabbitMQ 客户端

cmd/api
  读取配置、创建 RabbitMQ 连接、启动/关闭 Publisher 和 Consumer
```

HTTP Handler 不感知 RabbitMQ；Service 不直接调用 RabbitMQ；Publisher 不写业务主表；Consumer 不修改订单状态。

## 配置与故障响应

新增配置：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `RABBITMQ_URL` | 空（Compose 已设置） | RabbitMQ AMQP 连接串；设置后启动时必须连接成功。 |
| `RABBITMQ_EXCHANGE` | `user-order-api.events` | 持久化 Topic Exchange 名称。 |
| `RABBITMQ_AUDIT_QUEUE` | `user-order-api.audit.v1` | 审计 Consumer 队列名称。 |
| `OUTBOX_POLL_INTERVAL` | `1s` | Publisher 轮询间隔。 |
| `OUTBOX_BATCH_SIZE` | `100` | Publisher 单批领取上限，范围 1–1000。 |
| `OUTBOX_MAX_ATTEMPTS` | `10` | 超过后标记 `dead`。 |
| `CONSUMER_PREFETCH` | `20` | RabbitMQ Consumer 的未确认消息上限。 |
| `CONSUMER_MAX_RETRIES` | `5` | 超过后进入 DLQ。 |

RabbitMQ 错误不返回给 HTTP 客户端；业务事务失败使用现有 500 错误契约，消息链路故障通过日志、Outbox 状态和指标暴露。不得记录连接串中的密码。

## 可观测性

新增有限标签指标：

- `user_order_api_outbox_pending_total`
- `user_order_api_outbox_dead_total`
- `user_order_api_outbox_publish_total{status}`
- `user_order_api_outbox_publish_duration_seconds`
- `user_order_api_mq_consume_total{status}`
- `user_order_api_mq_consume_retry_total`
- `user_order_api_mq_consume_inflight`

禁止将 event payload、邮箱、Token、订单 ID 或用户 ID 放入 Prometheus label。日志必须包含 `request_id`、`event_id`、`event_type`、`consumer_name`、`attempt` 和 `trace_id`（存在时），不包含敏感字段。

## 验收标准

1. 未设置 `RABBITMQ_URL` 时，现有内存模式测试和宿主机启动行为不变。
2. 创建用户/订单或变更订单状态时，业务数据和 Outbox 事件同事务提交；模拟 Outbox 写失败时业务数据不落库。
3. RabbitMQ 正常时，Publisher Confirm 后 Outbox 才变为 `published`。
4. API 重启或 RabbitMQ 短暂不可用后，未发布事件可以继续投递，不丢失、不长期卡在 `publishing`。
5. 两个 API 实例不会长期领取同一个 Outbox 事件；已标记 `processed` 的重复投递只处理一次，崩溃恢复窗口允许 stdout 审计日志重复但不得丢失事件。
6. Consumer 失败按退避策略重试，超过次数进入 DLQ，HTTP 请求不被异步失败拖住。
7. `docker compose up --build -d` 后 API、MySQL、Redis、RabbitMQ、Prometheus、Jaeger、Alertmanager 均可启动；API `/readyz` 反映 MySQL 和已启用 MQ 的连接状态。
8. 只使用 `user_order_api_test` 做集成测试；不删除或重置 `user_order_api`，不执行 `docker compose down -v`。
9. `go test ./...`、`go vet ./...`、`go test -race ./...`、`docker compose config --quiet` 和 MQ 真实冒烟测试全部通过。
