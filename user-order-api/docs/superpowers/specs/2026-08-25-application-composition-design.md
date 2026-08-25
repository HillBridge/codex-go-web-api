# 应用组装层重构设计

## 目标

将 HTTP 应用的依赖组装从 `cmd/api/main.go` 与 `cmd/api/server.go` 收口到 `internal/app`，让启动入口、应用组装、领域实现和测试调用链各自清晰。

重构不改变 API 路径、认证授权规则、数据库表、配置项或业务行为。

## 当前问题

当前 `main.go` 创建 MySQL 连接、`auth.Service` 和管理员初始化；`server.go` 创建 User、Order 的 Service、Handler、路由和中间件，同时还包含内存认证适配器。这样同一种“依赖组装”职责分散在两个文件中，且测试需要依赖 `cmd/api` 包的私有构造函数。

## 目标结构

```text
cmd/api/
  main.go                 配置、MySQL 生命周期、HTTP 启停
  config.go               环境变量解析与 HTTP 配置

internal/app/
  application.go          Application、路由、HTTP 中间件
  production.go           MySQL Repository 和全部 Service 的生产组装
  memory.go               内存 Repository 和全部 Service 的测试组装
  application_test.go     内存 HTTP 调用链测试
  production_test.go      MySQL HTTP 调用链测试
```

`internal/auth`、`internal/user`、`internal/order` 保持领域边界，继续只提供模型、Repository 接口/实现、Service 和 Handler。`internal/platform` 保持通用基础设施。

## 组件与依赖

`internal/app` 导出以下入口：

- `NewProduction(ctx, db, logger, Config) (*Application, error)`：创建 MySQL 用户、订单和认证仓储；创建全部 Service、Handler、路由和安全中间件；必要时创建 bootstrap 管理员。
- `NewMemory(logger) *Application`：仅供测试创建共享的内存用户仓储、订单仓储和认证仓储，再创建相同的 HTTP 应用。认证身份适配器必须复用该用户仓储创建的用户 ID，保证注册后可立即创建订单。
- `NewWithDependencies(...) *Application`：仅供包内测试注入自定义 Repository、`auth.Service`、时钟或安全配置，验证边界条件。

`Config` 仅包含应用组装所需配置：JWT 签发参数、Refresh Token TTL、Cookie Secure 开关、CORS 来源、可信代理 CIDR、限流参数与 Bootstrap 管理员参数。网络监听和 HTTP 超时继续留在 `cmd/api` 的 `serverConfig`。

`Application` 继续实现 `http.Handler`，并保留 `Close(ctx)`，用于优雅排空异步审计日志。

## 三条运行路径

### 内存 HTTP 测试

```text
application_test.go
  → app.NewMemory
  → Memory User / Order / Auth Repository
  → Service + Handler + 路由
  → httptest 请求
```

不连接 MySQL，数据仅存在于测试进程内存。

### MySQL 集成测试

```text
production_test.go
  → MYSQL_TEST_DSN（必须是 user_order_api_test）
  → database.Open + ApplyMigrations
  → app.NewProduction
  → MySQL Repository
  → httptest 请求
```

测试库仍是实际 MySQL 实例，但只允许指向 `user_order_api_test`；测试创建的数据在清理阶段删除。

### 生产/本地运行

```text
cmd/api/main.go
  → MYSQL_DSN
  → database.Open + ApplyMigrations
  → app.NewProduction
  → newHTTPServer + serveUntilCancelled
```

生产与集成测试共用 MySQL Repository 与应用组装逻辑；唯一数据库差异是 DSN。

## 创建订单调用链

三种路径在应用创建完成后共享同一条订单请求链路：

```text
POST /api/v1/orders
  → Auth Bearer Middleware
  → order.Handler.orders
  → order.Service.Create
  → user.Repository.FindByID
  → order.Repository.Create
```

内存路径的两个 Repository 操作 map；MySQL 测试库和运行库路径分别对各自 DSN 对应的 `users`、`orders` 表执行 SQL。

## 测试策略

- `internal/auth`、`internal/user`、`internal/order` 的单元测试仍与实现同目录。这是 Go 的惯例，便于测试未导出的行为。
- 原 `cmd/api/server_test.go` 中的 HTTP 测试迁移至 `internal/app/application_test.go`；测试命名与断言保持不变。
- MySQL HTTP 集成测试迁移至 `internal/app/production_test.go`。`MYSQL_TEST_DSN` 缺失时仍通过既有 `testdb.RequireDSN` 跳过。
- `cmd/api` 保留 `config_test.go` 与 HTTP 生命周期测试；这些测试只验证入口职责。

## 错误处理与兼容性

- `NewProduction` 在 MySQL Repository 或 Bootstrap 管理员创建失败时返回错误，`main.go` 记录后退出。
- `Application.Close` 的审计日志排空语义不变。
- 重构不改变任何公开路由、HTTP 状态码、错误码、环境变量或数据库迁移。

## 验证

1. `go test ./...` 在未设置 `MYSQL_TEST_DSN` 时通过，MySQL 集成测试跳过。
2. 设置 `MYSQL_TEST_DSN` 后，MySQL Repository、迁移与完整 HTTP 登录/下单链路通过。
3. `go vet ./...` 与 `go test -race ./...` 通过。
4. 启动本地 MySQL 后，`go run ./cmd/api` 可完成注册、认证与创建订单。
