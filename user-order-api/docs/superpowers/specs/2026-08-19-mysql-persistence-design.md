# MySQL 持久化与游标分页设计

**目标：** 将用户与订单从进程内存迁移到 MySQL，提供可重复执行的数据库迁移，并为列表接口增加稳定的游标分页。

## 决策与边界

- 使用 Go 标准库 `database/sql` 与唯一新增的驱动依赖 `github.com/go-sql-driver/mysql`；不引入 ORM 或第三方迁移框架。
- 使用 MySQL 8.4，通过 Docker Compose 提供本地数据库服务；应用继续在宿主机以 `go run ./cmd/api` 启动。
- 生产启动必须提供 `MYSQL_DSN`。不保留“数据库不可用时自动退回内存仓储”的路径，以免悄然丢失数据。
- 迁移在应用启动、连接成功后执行；迁移失败则应用不启动。
- 列表接口由数组响应改为分页对象。这是有意的 API 非兼容变更。
- 本阶段不增加登录、权限、支付、订单状态流转或 Redis/消息队列。

## 本地运行方式

新增 `compose.yaml`，只包含 MySQL 服务与命名数据卷：

- 镜像：`mysql:8.4`
- 宿主机端口：`3307`，容器端口：`3306`
- 数据库：`user_order_api`
- 本地账号：`app` / `app_password`
- root 密码仅用于容器初始化，不由应用使用。

新增 `.env.example`，提供与 Compose 匹配的示例：

```text
MYSQL_DSN=app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC
```

真实凭据仅通过环境变量注入；`.env` 不提交。

## 启动与迁移

新增 `internal/platform/database`：

1. 从 `MYSQL_DSN` 打开连接池，设置合理的空闲/存活连接上限，并以带超时的 `PingContext` 验证连接。
2. 在同一连接池上执行嵌入式迁移文件。每个迁移文件按文件名升序执行，版本记录写入 `schema_migrations`。
3. 已执行版本跳过；每个迁移文件只包含一条 DDL 语句，成功执行后才记录版本。MySQL DDL 会隐式提交，因此迁移失败时应用启动失败，并通过新的前向迁移修复，不承诺 DDL 回滚。
4. 关闭应用时在 HTTP 与审计关闭之后关闭数据库连接池。

初始迁移创建以下表。

### `users`

- `id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY`
- `name VARCHAR(120) NOT NULL`
- `email VARCHAR(254) NOT NULL UNIQUE`
- `created_at DATETIME(6) NOT NULL`

邮箱仍由应用层小写和去空格；唯一索引作为最终并发约束。

### `orders`

- `id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY`
- `user_id BIGINT UNSIGNED NOT NULL`
- `amount BIGINT NOT NULL CHECK (amount > 0)`
- `status VARCHAR(16) NOT NULL`
- `created_at DATETIME(6) NOT NULL`
- `FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT`
- 索引 `(user_id, id)`，支持用户维度查询和未来扩展。

金额继续以最小货币单位的整数存储；本阶段不新增币种字段，后续支付阶段必须同时补齐币种语义。

## 应用层与仓储

保留 `user.Repository` 和 `order.Repository` 作为 service 的依赖端口，新增各自的 MySQL 实现。内存实现保留给 unit test 和教学，但不再由生产启动路径创建。

`user.Service` 与 `order.Service` 的业务校验不迁入 SQL。数据库仓储只负责：

- `Create`：插入并返回带数据库生成 ID、创建时间的模型；
- `FindByID`：将 `sql.ErrNoRows` 映射为各域的 `ErrNotFound`；
- `List`：按主键升序读取限定页；
- `Create` 的唯一键/外键错误映射为可识别的仓储错误，供 service 保持正确 HTTP 错误语义。

订单创建仍先通过 `UserFinder` 做友好校验；外键是并发和数据一致性的最终保障。

## 分页 API

`GET /users` 与 `GET /orders` 接受：

- `limit`：可选，默认 `20`，取值范围 `1`–`100`；
- `afterId`：可选正整数，表示只返回 ID 大于该值的数据。

响应统一为：

```json
{
  "items": [],
  "nextCursor": "42"
}
```

仓储查询最多读取 `limit + 1` 条。存在额外记录时返回最后一个实际条目的 ID 作为 `nextCursor`；否则省略该字段。下一页将该值作为 `afterId` 传回。

创建与按 ID 查询接口的请求、响应和状态码保持不变。

## 错误处理与可观测性

- 缺少或不可用的 `MYSQL_DSN` 使进程启动失败，并输出不含密码的错误信息。
- 无效分页参数返回 JSON `400`。
- 重复邮箱继续返回 `400 email already exists`。
- 数据库超时、连接和未知 SQL 错误保持为内部错误，不伪装为用户不存在或其他客户端错误。
- 启动日志记录数据库已连接和已应用迁移版本，但不记录 DSN 或密码。

## 测试与验收

- 单元测试：分页参数解析、分页结果、仓储错误映射和 service 的业务校验。
- 集成测试：在 Compose 启动的 MySQL 上验证迁移可重复执行、唯一邮箱、外键约束、创建/查询和游标分页。
- 回归检查：`go test ./...`、`go vet ./...`、`go test -race ./...`。
- 运行验证：启动 Compose、以示例 DSN 启动 API、执行用户创建、订单创建和两页列表请求。

## 非目标

- 不迁移已有内存数据；当前项目没有持久化历史数据。
- 不支持迁移回滚；迁移只向前演进。
- 不实现多实例迁移锁、读写分离、分库分表或连接代理。
