# MySQL 持久化实施计划（中文版）

> **执行要求：** 按任务逐项实施，并使用复选框追踪进度。每项行为变更必须先写失败测试，再写最小实现。

**目标：** 让 API 使用 MySQL 持久化用户和订单数据，支持仅向前的嵌入式迁移，以及用户和订单列表的游标分页。

**架构：** `cmd/api/main.go` 读取必填的 `MYSQL_DSN`，打开一个 MySQL 连接池并执行迁移，然后将 MySQL 仓储注入现有 service 层。仓储接口使用中立的通用分页契约；handler 解析 `limit` 和 `afterId`，返回 `{items, nextCursor}`。内存仓储只保留给单元测试和教学。

**技术栈：** Go 1.25.3、`database/sql`、`github.com/go-sql-driver/mysql`、MySQL 8.4、Docker Compose。

**对应规格：** [MySQL 持久化与游标分页设计](../specs/2026-08-19-mysql-persistence-design.md)

## 全局约束

- 仅新增 `github.com/go-sql-driver/mysql`，不引入 ORM 或第三方迁移框架。
- 生产启动必须提供 `MYSQL_DSN`，不得在数据库不可用时自动回退到内存仓储。
- MySQL DDL 迁移仅向前执行；每个嵌入式迁移文件只包含一条 DDL 语句。
- 创建和按 ID 查询接口的成功响应保持不变。
- 列表接口有意改为 `{items, nextCursor}`，并支持 `limit`（1–100）和 `afterId`。
- 每次验证均使用独立的 Go 构建缓存。
- 未经用户明确要求不创建提交。

---

## 任务 1：MySQL 配置与本地开发环境

**涉及文件：**

- 新建：`compose.yaml`、`.env.example`
- 修改：`.gitignore`、`go.mod`、`cmd/api/config.go`、`cmd/api/config_test.go`、`README.md`

**新增接口：**

- `serverConfig` 新增 `MySQLDSN string`。
- 当环境变量为空时，`loadConfig(getenv)` 返回 `MYSQL_DSN is required`。

- [x] **步骤 1：编写失败的配置测试**

  用 `MYSQL_DSN=app:secret@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC` 测试 DSN 被原样保存到 `config.MySQLDSN`；再测试未提供 `MYSQL_DSN` 时返回准确错误。

- [x] **步骤 2：验证测试因缺少字段和校验而失败**

  ```bash
  env GOCACHE=<临时可写目录> go test ./cmd/api -run TestLoadConfig
  ```

  预期：先出现 `MySQLDSN` 未定义的编译失败；补齐字段后，未配置 DSN 的用例仍会失败，直到实现校验。

- [x] **步骤 3：实现配置与本地 MySQL 文件**

  在现有 HTTP 超时解析后增加 `MYSQL_DSN` 校验。创建一个 `mysql:8.4` 的 Compose 服务：端口 `3307:3306`、数据库 `user_order_api`、应用账号 `app` / `app_password`、root 密码 `root_password`、`mysqladmin ping` 健康检查以及 `mysql_data` 命名卷。

  将 `.env` 加入 `.gitignore`，在 `.env.example` 写入 Compose 对应 DSN；通过 `go get github.com/go-sql-driver/mysql` 增加驱动；在 README 说明 `docker compose up -d` 与 `MYSQL_DSN` 的使用方式。

- [x] **步骤 4：验证任务 1**

  ```bash
  env GOCACHE=<临时可写目录> go test ./cmd/api -run TestLoadConfig
  ```

  预期：全部配置测试通过。

## 任务 2：连接池与嵌入式向前迁移

**涉及文件：**

- 新建：`internal/platform/database/mysql.go`
- 新建：`internal/platform/database/migrate.go`
- 新建：`internal/platform/database/migrations/0001_users.sql`
- 新建：`internal/platform/database/migrations/0002_orders.sql`
- 新建：`internal/platform/database/database_integration_test.go`
- 修改：`cmd/api/main.go`、`go.mod`

**新增接口：**

```go
database.Open(ctx context.Context, dsn string) (*sql.DB, error)
database.ApplyMigrations(ctx context.Context, db *sql.DB) error
```

集成测试读取 `MYSQL_TEST_DSN`；未配置时使用 `t.Skip` 跳过，不影响普通单元测试。

- [x] **步骤 1：编写失败的迁移集成测试**

  在 `MYSQL_TEST_DSN` 存在时，打开测试数据库，连续执行两次 `ApplyMigrations`，断言 `users`、`orders` 与两条迁移记录存在。另增加一条向不存在的 `user_id` 插入订单的 SQL，并断言 MySQL 错误码为 `1452`，证明外键生效。

- [x] **步骤 2：验证测试因数据库包缺失而失败**

  ```bash
  MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<临时可写目录> go test ./internal/platform/database -run TestApplyMigrations
  ```

  预期：实现前出现 `database.Open` 或 `database.ApplyMigrations` 缺失的错误。

- [x] **步骤 3：实现连接池、迁移与生命周期接入**

  `Open` 设置最大连接数 10、最大空闲连接 5、连接最大生命周期 30 分钟，并用 5 秒 `PingContext` 校验连接。嵌入并按文件名字典序读取 `migrations/*.sql`；先创建 `schema_migrations(version VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL)`，再对未记录的文件执行 DDL，成功后才写入版本记录。

  `0001_users.sql` 创建：

  ```sql
  users(id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        name VARCHAR(120) NOT NULL,
        email VARCHAR(254) NOT NULL UNIQUE,
        created_at DATETIME(6) NOT NULL)
  ```

  `0002_orders.sql` 创建：

  ```sql
  orders(id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
         user_id BIGINT UNSIGNED NOT NULL,
         amount BIGINT NOT NULL CHECK (amount > 0),
         status VARCHAR(16) NOT NULL,
         created_at DATETIME(6) NOT NULL,
         CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
         INDEX idx_orders_user_id (user_id, id))
  ```

  `main` 在构建应用前打开并迁移 MySQL。先注册 `db.Close()` 的 defer，再注册应用关闭的 defer，使实际顺序为 HTTP → 审计 → 数据库。

- [x] **步骤 4：验证任务 2**

  ```bash
  docker compose up -d
  MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC' \
    env GOCACHE=<临时可写目录> go test ./internal/platform/database -run TestApplyMigrations
  ```

  预期：迁移可重复执行，外键约束通过验证。

## 任务 3：MySQL 仓储与生产依赖注入

**涉及文件：**

- 新建：`internal/user/mysql_repository.go`、`internal/order/mysql_repository.go`
- 新建：`internal/user/mysql_repository_integration_test.go`
- 新建：`internal/order/mysql_repository_integration_test.go`
- 修改：`internal/order/repository.go`、`internal/order/service.go`
- 修改：`cmd/api/server.go`、`cmd/api/main.go`

**新增接口：**

```go
user.NewMySQLRepository(db *sql.DB) user.Repository
order.NewMySQLRepository(db *sql.DB) order.Repository
newApplication(logger *slog.Logger, userRepo user.Repository, orderRepo order.Repository) *application
```

MySQL 错误码 `1062` 映射为 `user.ErrEmailTaken`；外键错误码 `1452` 映射为 `order.ErrUserNotFound`。

- [x] **步骤 1：编写失败的仓储集成测试**

  验证：创建用户会保存小写邮箱；重复邮箱返回 `user.ErrEmailTaken`；`FindByID` 的无记录结果映射为各域的 `ErrNotFound`；创建订单会持久化 `pending` 状态；为不存在用户创建订单返回 `order.ErrUserNotFound`。

- [x] **步骤 2：验证测试失败**

  ```bash
  MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<临时可写目录> go test ./internal/user ./internal/order -run MySQL
  ```

  预期：因为 MySQL 仓储构造函数尚不存在而失败。

- [x] **步骤 3：实现仓储并注入生产应用**

  仓储仅使用 `ExecContext`、`QueryRowContext`。使用 `UTC_TIMESTAMP(6)` 写入创建时间，读取 `LastInsertId` 后查询完整记录。将 `sql.ErrNoRows`、`1062` 和 `1452` 分别转换为领域错误。

  `order.Service.Create` 仅把 `ErrUserNotFound` 转为现有的用户不存在客户端错误；连接超时和未知 SQL 错误必须继续作为内部错误。`newApplication` 改为接收仓储；`newServer` 仅作为内存仓储测试构造器；`main` 注入两个 MySQL 仓储。

- [x] **步骤 4：验证任务 3**

  ```bash
  MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<临时可写目录> \
    go test ./internal/user ./internal/order ./cmd/api -run 'MySQL|TestUserAndOrderFlow|TestRejectsOrderForMissingUser'
  ```

  预期：数据库约束、仓储行为与既有 HTTP 业务路径均通过。

## 任务 4：仓储、服务与 HTTP 的游标分页

**涉及文件：**

- 新建：`internal/platform/page/page.go`、`internal/platform/page/page_test.go`
- 修改：`internal/platform/httpx/httpx.go`、`internal/platform/httpx/httpx_test.go`
- 修改：`internal/user/model.go`、`internal/order/model.go`
- 修改：两个领域的 repository、service、handler 和 MySQL repository
- 修改：`cmd/api/server_test.go`、`README.md`

**新增接口：**

```go
page.Request{Limit int, AfterID int64}
page.Result[T]{Items []T, NextCursor string}
httpx.ParsePageRequest(r *http.Request) (page.Request, error)
```

用户与订单仓储的 `List` 改为：

```go
List(ctx context.Context, request page.Request) (page.Result[T], error)
```

- [x] **步骤 1：编写失败的分页测试**

  覆盖默认分页、`limit=3&afterId=12`、`limit=0`、`limit=101`、非法或为零的 `afterId`。内存仓储用 ID 1–3 验证：请求 `{Limit: 2}` 返回 `[1,2]` 和 `NextCursor="2"`；请求 `{Limit: 2, AfterID: 2}` 返回 `[3]` 且没有游标。HTTP 测试断言 `GET /users?limit=1` 返回 `{"items":[...],"nextCursor":"1"}`，非法参数返回 JSON 400。

- [x] **步骤 2：验证分页测试失败**

  ```bash
  env GOCACHE=<临时可写目录> go test ./internal/platform/page ./internal/platform/httpx ./internal/user ./internal/order ./cmd/api \
    -run 'TestParsePageRequest|Test.*Pagination|TestUsersList'
  ```

  预期：页码类型、解析函数缺失，随后在 handler 仍返回数组时出现响应不匹配。

- [x] **步骤 3：实现分页契约和双存储路径**

  `page.Result[T]` 使用 `json:"items"` 与 `json:"nextCursor,omitempty"`。`httpx` 解析查询参数，领域包不得导入 `net/http`。内存仓储排序后按 `ID > AfterID` 过滤，读取 `Limit + 1` 条，并仅在存在下一页时返回最后一个实际项目 ID。MySQL 使用：

  ```sql
  WHERE id > ? ORDER BY id ASC LIMIT ?
  ```

  service 和 handler 使用分页契约；POST 与 `/:id` 保持不变。更新 README 和架构文档中的分页示例。

- [x] **步骤 4：验证任务 4**

  ```bash
  MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<临时可写目录> go test ./...
  ```

  预期：内存与 MySQL 分页测试、HTTP 测试以及创建/查询路径都通过。

## 任务 5：端到端验收与文档复核

**涉及文件：** `README.md`、`docs/architecture.md`、`docs/images/architecture-overview.svg`。

- [x] **步骤 1：运行本地验收链路**

  启动 `docker compose up -d`，导出 Compose DSN，并执行 `go run ./cmd/api`。创建两个用户，为第一个用户创建订单，调用 `GET /users?limit=1`，再使用返回的 `nextCursor` 调用 `GET /users?limit=1&afterId=<nextCursor>`。重启 API 后确认用户和订单仍存在。

- [x] **步骤 2：更新文档**

  记录 Compose 启动方式、必填 `MYSQL_DSN`、启动时迁移、持久化行为和分页响应。将架构图中的内存 map 更新为 MySQL、连接池与嵌入式迁移文件。

- [x] **步骤 3：执行最终验证**

  ```bash
  gofmt -d cmd/api/*.go internal/**/*.go
  git diff --check
  env GOCACHE=<临时可写目录> go test ./...
  env GOCACHE=<临时可写目录> go vet ./...
  env GOCACHE=<临时可写目录> go test -race ./...
  ```

  预期：格式化无输出，测试、静态检查和竞态检查全部通过；配置 `MYSQL_TEST_DSN` 时运行数据库集成测试。
