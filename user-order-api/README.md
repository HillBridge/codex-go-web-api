# User Order API

这是一个给前端同学练 Go 后端基础的小项目。

第一阶段刻意只使用 Go 标准库，不引入 Gin、GORM、Redis、数据库等外部依赖，目的是把 Go 后端最基础的分层、接口、错误处理、上下文、并发和测试先练扎实。

## 运行

```bash
go run ./cmd/api
```

服务启动后访问：

```text
http://localhost:8888
```

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
curl http://localhost:8888/users
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
curl http://localhost:8888/orders
```

### 查询订单详情

```bash
curl http://localhost:8888/orders/1
```

## 项目结构

```text
cmd/api
  main.go          程序入口
  server.go        组装路由、service、repository
  server_test.go   HTTP 核心链路测试

internal/user
  model.go         User 和 CreateUserRequest
  repository.go    用户存储接口和内存实现
  service.go       用户业务逻辑、参数校验、错误处理
  handler.go       HTTP handler

internal/order
  model.go         Order、Status 和 CreateOrderRequest
  repository.go    订单存储接口和内存实现
  service.go       订单业务逻辑，依赖 UserFinder interface
  handler.go       HTTP handler

internal/platform
  audit            异步审计日志，演示 goroutine/context/defer
  httpx            JSON、错误响应、路径参数工具
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

`audit.AsyncLogger` 用 goroutine 异步记录审计日志，演示“不阻塞主业务”的后台任务。

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
