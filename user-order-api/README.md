# User Order API

这是一个给前端同学练 Go 后端基础的小项目。

项目保留标准库 HTTP 分层，并使用 MySQL 持久化用户和订单数据；不引入 Gin、GORM、Redis 或 ORM，以便把 Go 后端的分层、接口、错误处理、上下文、并发、SQL 和迁移基础练扎实。

## 运行

```bash
docker compose up -d
export MYSQL_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC'
go run ./cmd/api
```

服务启动后访问：

```text
http://localhost:8888
```

运行参数：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MYSQL_DSN` | 无，必填 | MySQL 连接串；应用启动时会执行尚未应用的迁移。 |
| `PORT` | `8888` | HTTP 监听端口，范围为 1–65535。 |
| `READ_HEADER_TIMEOUT` | `5s` | 读取请求头超时。 |
| `READ_TIMEOUT` | `15s` | 读取整个请求超时。 |
| `WRITE_TIMEOUT` | `15s` | 写响应超时。 |
| `IDLE_TIMEOUT` | `60s` | keep-alive 空闲连接超时。 |
| `SHUTDOWN_TIMEOUT` | `10s` | 收到中断或 `SIGTERM` 后的优雅关闭等待时间。 |

超时变量使用 Go duration 格式，例如 `WRITE_TIMEOUT=20s`。本地 MySQL 配置见 [.env.example](.env.example)；真实凭据应通过环境变量注入，`.env` 不会提交。服务会在收到中断或 `SIGTERM` 时停止接收新请求，并等待在途请求完成。

健康检查：

```bash
curl http://localhost:8888/health
```

## 测试

```bash
go test ./...
```

## API

### 创建用户

```bash
curl -X POST http://localhost:8888/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","email":"ada@example.com"}'
```

### 查询用户列表

```bash
curl 'http://localhost:8888/users?limit=20'
```

列表响应使用游标分页：`limit` 默认为 20，范围 1–100；首个请求可省略 `afterId`，后续请求将返回的 `nextCursor` 作为 `afterId` 传回。没有下一页时不会出现 `nextCursor`。

```json
{"items":[{"id":1,"name":"Ada","email":"ada@example.com"}],"nextCursor":"1"}
```

### 查询用户详情

```bash
curl http://localhost:8888/users/1
```

### 创建订单

```bash
curl -X POST http://localhost:8888/orders \
  -H 'Content-Type: application/json' \
  -d '{"userId":1,"amount":2599}'
```

`amount` 使用整数表示最小货币单位，比如 2599 表示 25.99 元，避免浮点数金额误差。

### 查询订单列表

```bash
curl 'http://localhost:8888/orders?limit=20'
```

### 查询订单详情

```bash
curl http://localhost:8888/orders/1
```

## 项目结构

完整的启动流程、分层职责和模块依赖关系见 [架构说明](docs/architecture.md)。

```text
cmd/api
  main.go          程序入口
  server.go        组装路由、service、repository
  server_test.go   HTTP 核心链路测试

internal/user
  model.go         User 和 CreateUserRequest
  repository.go    用户存储接口和内存实现（测试）
  mysql_repository.go MySQL 持久化实现
  service.go       用户业务逻辑、参数校验、错误处理
  handler.go       HTTP handler

internal/order
  model.go         Order、Status 和 CreateOrderRequest
  repository.go    订单存储接口和内存实现（测试）
  mysql_repository.go MySQL 持久化实现
  service.go       订单业务逻辑，依赖 UserFinder interface
  handler.go       HTTP handler

internal/platform
  audit            有界队列与固定 worker 的异步审计日志；队列满时最佳努力丢弃并记录告警
  database         连接池、嵌入式向前迁移及 SQL 文件
  httpx            JSON、错误响应、路径参数工具
  page             游标分页请求与响应契约
```

## 这版练到哪些 Go 基础

### struct

用于表达业务对象和依赖：

```text
User
Order
CreateUserRequest
CreateOrderRequest
UserService
OrderService
```

### interface

用于定义模块边界：

```text
user.Repository
order.Repository
order.UserFinder
audit.Logger
```

这样 service 不直接绑定具体存储实现，后续可以把内存 repository 换成 MySQL/PostgreSQL repository。

### error

用于显式表达失败：

```text
user.ErrNotFound
user.ErrEmailTaken
order.ErrNotFound
httpx.AppError
```

service 层把底层错误翻译成 HTTP 友好的业务错误。

### context.Context

每个 HTTP handler 都创建带超时的 context：

```go
context.WithTimeout(r.Context(), 2*time.Second)
```

然后传给 service 和 repository。

这模拟真实后端里请求超时、取消、数据库查询取消的工作方式。

### goroutine

`audit.AsyncLogger` 使用有界队列和固定 worker goroutine 异步记录审计日志；队列满时不阻塞主业务，服务关闭时会在关闭窗口内排空已入队事件。

### defer

用于释放 context：

```go
ctx, cancel := context.WithTimeout(...)
defer cancel()
```

后续接数据库时，也会用于关闭 rows、事务 rollback、关闭响应体等。

### slice/map

内存 repository 用 map 存数据，用 slice 返回列表：

```text
map[int64]User
map[int64]Order
[]User
[]Order
```

### package

按后端职责拆包：

```text
handler
service
repository
model
platform
```

## 下一阶段建议

第二阶段可以加数据库：

```text
PostgreSQL / MySQL
database/sql
表设计
主键和唯一索引
事务
数据库迁移
分页查询
```

第三阶段可以加可靠性能力：

```text
JWT 登录
RBAC 权限
订单状态机
幂等 key
Redis 缓存
异步任务队列
结构化日志
OpenAPI 文档
Docker 部署
```
