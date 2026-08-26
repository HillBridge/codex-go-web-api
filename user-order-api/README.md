# User Order API

这是一个给前端同学练 Go 后端基础的小项目。

项目保留标准库 HTTP 分层，并使用 MySQL 持久化用户和订单数据；不引入 Gin、GORM、Redis 或 ORM，以便把 Go 后端的分层、接口、错误处理、上下文、并发、SQL 和迁移基础练扎实。

## 运行

最简单的本地启动方式：

```bash
./scripts/start-local.sh
```

脚本只会启动本地 MySQL、设置 Compose 对应的默认 `MYSQL_DSN`，再在宿主机运行 API。若需要连接其他数据库，可在启动前设置自己的 `MYSQL_DSN`。

也可以手动执行：

```bash
docker compose up -d
export MYSQL_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC'
export JWT_SIGNING_KEY='replace-with-at-least-32-random-bytes'
go run ./cmd/api
```

服务启动后访问：

```text
http://localhost:8888
```

### Docker Compose（API、MySQL、Prometheus）

需要以容器方式运行整个本地栈时，执行：

```bash
docker compose up --build -d
docker compose ps
```

这会启动 API（`http://localhost:8888`）、MySQL（供 Navicat 使用的 `127.0.0.1:3307`）和 Prometheus（`http://localhost:9090`）。容器内的 API 通过内部地址 `mysql:3306` 访问数据库，外部不应在生产环境映射 MySQL 端口。

常用操作：

```bash
docker compose logs -f api
docker compose down
```

`docker compose down` 仅停止和移除容器，会保留本地 MySQL 与 Prometheus 数据卷。**不要在日常开发执行 `docker compose down -v`**，它会删除这些本地数据卷。

### 健康检查与指标

```bash
curl http://localhost:8888/healthz
curl http://localhost:8888/readyz
curl http://localhost:8888/metrics | rg 'user_order_api_http_requests_total'
```

- `/healthz`：存活检查，只确认 API 进程可以响应。
- `/readyz`：就绪检查，额外确认 MySQL 可连接；依赖不可用时返回 `503`，不回显数据库错误。
- `/metrics`：Prometheus 指标端点，仅用于本地或受保护的监控网络。

Prometheus 每 15 秒从 API 的 `/metrics` 采集一次。打开 `http://localhost:9090` 后可查询：

```promql
sum(rate(user_order_api_http_requests_total[5m])) by (route, status)
histogram_quantile(0.95, sum(rate(user_order_api_http_request_duration_seconds_bucket[5m])) by (le, route))
user_order_api_mysql_in_use_connections
user_order_api_audit_queue_pending
```

运行参数：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MYSQL_DSN` | 无，必填 | MySQL 连接串；应用启动时会执行尚未应用的迁移。 |
| `JWT_SIGNING_KEY` | 无，必填 | 至少 32 字节的随机签名密钥。可用 `openssl rand -hex 32` 生成。 |
| `JWT_ISSUER` | `user-order-api` | Access JWT 的签发者。 |
| `ACCESS_TOKEN_TTL` | `15m` | Access JWT 有效期。 |
| `REFRESH_TOKEN_TTL` | `168h` | 服务端可撤销 Refresh 会话有效期。 |
| `AUTH_COOKIE_SECURE` | `true` | 生产必须为 `true`；本地 HTTP 开发才显式设为 `false`。 |
| `CORS_ALLOWED_ORIGINS` | 空 | 逗号分隔的精确前端 Origin 白名单；默认拒绝跨域。 |
| `TRUSTED_PROXY_CIDRS` | 空 | 逗号分隔的反向代理 CIDR；仅这些代理转发的 `X-Forwarded-For` 会用于限流来源 IP。 |
| `RATE_LIMIT_LOGIN_PER_MINUTE` | `5` | 单 IP 登录/注册每分钟上限。 |
| `RATE_LIMIT_REFRESH_PER_MINUTE` | `20` | 单 IP 刷新会话每分钟上限。 |
| `RATE_LIMIT_API_PER_MINUTE` | `120` | 单 IP 普通 API 每分钟上限。 |
| `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` | 均为空 | 两者同时设置时，仅在该邮箱不存在时创建首个管理员。 |
| `PORT` | `8888` | HTTP 监听端口，范围为 1–65535。 |
| `READ_HEADER_TIMEOUT` | `5s` | 读取请求头超时。 |
| `READ_TIMEOUT` | `15s` | 读取整个请求超时。 |
| `WRITE_TIMEOUT` | `15s` | 写响应超时。 |
| `IDLE_TIMEOUT` | `60s` | keep-alive 空闲连接超时。 |
| `SHUTDOWN_TIMEOUT` | `10s` | 收到中断或 `SIGTERM` 后的优雅关闭等待时间。 |

超时变量使用 Go duration 格式，例如 `WRITE_TIMEOUT=20s`。本地 MySQL 配置见 [.env.example](.env.example)；真实凭据应通过环境变量注入，`.env` 不会提交。服务会在收到中断或 `SIGTERM` 时停止接收新请求，并等待在途请求完成。

健康检查：

```bash
curl http://localhost:8888/api/v1/health
```

## 测试

```bash
go test ./...
```

MySQL 集成测试只允许使用独立的 `user_order_api_test` 数据库；测试会重置其中的表，绝不能把 `MYSQL_TEST_DSN` 指向开发库 `user_order_api`。首次运行或已有本地 MySQL 数据卷时，先创建测试库：

```bash
./scripts/setup-test-database.sh
export MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC'
go test ./...
```

## OpenAPI 与 Postman

- OpenAPI 3.0 接口契约：[docs/openapi.yaml](docs/openapi.yaml)
- Postman Collection：[docs/postman/user-order-api.postman_collection.json](docs/postman/user-order-api.postman_collection.json)
- 实施阶段与完成状态：[docs/implementation-roadmap.md](docs/implementation-roadmap.md)
- 本地 Docker 与 Prometheus 使用说明：[docs/local-docker-prometheus.md](docs/local-docker-prometheus.md)
- 阶段 6 认证、授权与基础安全说明：[docs/phase-6-authentication-authorization-security.md](docs/phase-6-authentication-authorization-security.md)

在 Postman 点击 **Import**，选择该 Collection 文件即可。先调用 **Auth / Register**：它会自动保存 `accessToken`、`userId`，Postman 也会保存服务设置的 Refresh Cookie。随后可调用 **Orders / Create Order**；它会生成幂等键和 `orderId`。**Refresh** 会轮换 Access Token，**Replay Create Order** 可验证网络重试不会重复创建订单。

完整验收请按顺序运行 **Stage 7 Acceptance** 文件夹：它会注册用户 A/B、验证 B 访问 A 的订单返回 `403`、验证 Refresh 与 Logout 后旧 Access Token 立即返回 `401`。其中管理员步骤要求服务启动前已设置 `BOOTSTRAP_ADMIN_EMAIL`、`BOOTSTRAP_ADMIN_PASSWORD`，并将相同值填入 Collection Variables 的 `adminEmail`、`adminPassword`。

可直接用下列命令启动阶段 7 验收服务；它会使用专用测试库、允许本地 HTTP Cookie，并默认创建 `stage7-admin@example.com` / `correct-password` 管理员：

```bash
./scripts/start-stage7.sh
```

如需使用其他管理员账号，在命令前设置 `BOOTSTRAP_ADMIN_EMAIL`、`BOOTSTRAP_ADMIN_PASSWORD`；Postman 的 `adminEmail`、`adminPassword` 必须填入相同值。

## API

### 注册、登录与 Token 使用

先注册并把响应中的 `accessToken` 暂时放进终端变量。浏览器前端应仅把 Access Token 放在内存中，不能写入 `localStorage`；Refresh Token 是 HttpOnly Cookie，JavaScript 不可读取。

```bash
curl -c cookies.txt -X POST http://localhost:8888/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","email":"ada@example.com","password":"correct-password"}'
```

登录、刷新和退出：

```bash
curl -c cookies.txt -X POST http://localhost:8888/api/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"ada@example.com","password":"correct-password"}'
curl -b cookies.txt -c cookies.txt -X POST http://localhost:8888/api/v1/auth/refresh
curl -b cookies.txt -X POST http://localhost:8888/api/v1/auth/logout
```

之后每个受保护请求携带：`-H "Authorization: Bearer $ACCESS_TOKEN"`。缺失、无效、过期或所属会话已撤销的令牌返回 `401 UNAUTHENTICATED`；越权返回 `403 FORBIDDEN`。调用 logout 后，该会话的 Access Token 在下一次请求立即失效。

### 创建用户（仅管理员）

```bash
curl -X POST http://localhost:8888/api/v1/users \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -d '{"name":"Ada","email":"ada@example.com"}'
```

### 查询用户列表

```bash
curl -H "Authorization: Bearer $ACCESS_TOKEN" 'http://localhost:8888/api/v1/users?limit=20'
```

列表响应使用游标分页：`limit` 默认为 20，范围 1–100；首个请求可省略 `afterId`，后续请求将返回的 `nextCursor` 作为 `afterId` 传回。没有下一页时不会出现 `nextCursor`。

```json
{"items":[{"id":1,"name":"Ada","email":"ada@example.com"}],"nextCursor":"1"}
```

### 查询用户详情

```bash
curl -H "Authorization: Bearer $ACCESS_TOKEN" http://localhost:8888/api/v1/users/1
```

### 创建订单

```bash
curl -X POST http://localhost:8888/api/v1/orders \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Idempotency-Key: create-order-20260820-001' \
  -d '{"userId":1,"amount":2599}'
```

`amount` 使用人民币（CNY）分作为最小货币单位；例如 2599 表示 ¥25.99，避免浮点数金额误差。时间字段均为 UTC RFC 3339 格式。当前版本不支持其他币种。

`Idempotency-Key` 是可选请求头。传入后，服务会去除首尾空白，并要求其长度为 1–255；同一个 key 携带相同 `userId` 与 `amount` 重试时返回同一订单及 `200`，携带不同参数时返回 `409 IDEMPOTENCY_KEY_CONFLICT`。不传该头时，每次请求都会创建新订单。

### 订单状态机

新订单状态为 `pending`，仅允许如下流转：

```text
pending --支付--> paid
pending --取消--> cancelled
```

`paid` 与 `cancelled` 均为终态。重复执行同一个动作会返回当前订单和 `200`；对已支付订单取消、或对已取消订单支付，会返回 `409 INVALID_ORDER_STATE`。

```bash
curl -X POST -H "Authorization: Bearer $ACCESS_TOKEN" http://localhost:8888/api/v1/orders/1/pay
curl -X POST -H "Authorization: Bearer $ACCESS_TOKEN" http://localhost:8888/api/v1/orders/1/cancel
```

### 查询订单列表

```bash
curl -H "Authorization: Bearer $ACCESS_TOKEN" 'http://localhost:8888/api/v1/orders?limit=20'
```

### 查询订单详情

```bash
curl -H "Authorization: Bearer $ACCESS_TOKEN" http://localhost:8888/api/v1/orders/1
```

## 项目结构

完整的启动流程、分层职责和模块依赖关系见 [架构说明](docs/architecture.md)。

```text
cmd/api
  main.go          读取配置、打开/关闭 MySQL、启动/关闭 HTTP Server
  config.go        环境变量解析与 HTTP 超时配置

internal/app
  application.go   路由、HTTP 中间件与 Handler/Service 组装
  composition.go   NewMemory（测试）与 NewProduction（MySQL）组装入口
  http_test.go     内存 HTTP 调用链与 MySQL HTTP 集成测试
  test_helpers_test.go  测试专用依赖注入辅助函数

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

internal/auth
  service.go       bcrypt 注册/登录、JWT 签发、Refresh 轮换与退出
  repository.go    会话与身份仓储接口；Refresh Token 仅保存哈希
  handler.go       注册、登录、刷新、退出和当前用户接口
  middleware.go    Bearer JWT 校验并注入请求身份

internal/platform
  audit            有界队列与固定 worker 的异步审计日志；队列满时最佳努力丢弃并记录告警
  database         连接池、嵌入式向前迁移及 SQL 文件
  httpx            JSON、错误响应、路径参数工具
  page             游标分页请求与响应契约
  principal        与业务解耦的请求身份上下文
  security         精确 Origin CORS 与按 IP 的内存限流
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

这样 service 不直接绑定具体存储实现。当前项目使用 MySQL 作为运行时持久化存储，内存 Repository 只用于快速单元测试和 HTTP 测试；将来如需 PostgreSQL，可在保持 Service/Handler 不变的前提下新增 Repository 实现。

应用组装位于 `internal/app`：`NewMemory` 创建共享的内存用户、订单和认证仓储，`NewProduction` 创建 MySQL 仓储。两者都会构造相同的 Handler、Service、路由和中间件。

### error

用于显式表达失败：

```text
user.ErrNotFound
user.ErrEmailTaken
order.ErrNotFound
httpx.AppError
```

service 层把底层错误翻译成 HTTP 友好的业务错误。

所有错误响应均携带稳定的 `code` 与面向人类的 `error` 文案，例如：

```json
{"code":"USER_NOT_FOUND","error":"user does not exist"}
```

前端应依据 `code` 处理分支，而非依赖可能调整的 `error` 文案。

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

后续可以继续深化数据库能力：

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
