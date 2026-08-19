# 项目架构与依赖关系

本文基于当前代码实现说明 `user-order-api` 的启动流程、分层职责与模块依赖。

## 总览

项目是一个仅使用 Go 标准库的 HTTP API 服务。`cmd/api` 是程序入口，`newServer()` 是组合根：它创建具体实现，并将依赖注入到 handler 和 service 中。

![项目架构与依赖关系](images/architecture-overview.svg)

<details>
<summary>查看可编辑的 Mermaid 源</summary>

```mermaid
flowchart TB
    client["客户端 / 前端"]

    subgraph entry["启动与依赖组装"]
        main["cmd/api/main.go<br/>main()"]
        server["net/http.Server<br/>:8888"]
        compose["cmd/api/server.go<br/>newServer() · 组合根"]
        mux["http.ServeMux"]
        middleware["requestLogMiddleware<br/>请求结构化日志"]
    end

    subgraph routes["HTTP 路由层"]
        health["GET /health"]
        userHandler["internal/user/Handler<br/>/users · /users/:id"]
        orderHandler["internal/order/Handler<br/>/orders · /orders/:id"]
    end

    subgraph userDomain["用户域：internal/user"]
        userService["Service<br/>校验、错误翻译"]
        userRepoPort["Repository interface"]
        userRepo["MemoryRepository<br/>map 用户 / 邮箱索引"]
    end

    subgraph orderDomain["订单域：internal/order"]
        orderService["Service<br/>校验、用户存在性检查"]
        orderRepoPort["Repository interface"]
        orderRepo["MemoryRepository<br/>map 订单"]
        userFinder["UserFinder interface<br/>FindByID"]
    end

    subgraph platform["平台公共能力：internal/platform"]
        httpx["httpx<br/>JSON 解码 / 响应 / AppError / 路径 ID"]
        auditPort["audit.Logger interface"]
        auditLogger["audit.AsyncLogger<br/>有界队列 + 固定 worker"]
    end

    subgraph runtime["运行时资源"]
        memory["进程内存<br/>数据随进程停止而丢失"]
        stdout["标准输出<br/>slog 文本日志"]
    end

    main -->|"创建"| server
    main -->|"调用"| compose
    compose -->|"返回 Handler"| server
    client -->|"HTTP 请求"| server
    server --> middleware --> mux

    compose -. "注册" .-> health
    compose -. "注册" .-> userHandler
    compose -. "注册" .-> orderHandler

    health --> httpx
    userHandler -->|"创建 2 秒 Context"| userService
    orderHandler -->|"创建 2 秒 Context"| orderService
    userHandler --> httpx
    orderHandler --> httpx

    compose -. "注入" .-> userService
    compose -. "注入" .-> orderService
    compose -. "创建" .-> userRepo
    compose -. "创建" .-> orderRepo
    compose -. "创建并注入" .-> auditLogger

    userService --> userRepoPort --> userRepo --> memory
    orderService --> orderRepoPort --> orderRepo --> memory
    orderService --> userFinder
    userFinder -->|"由 User Service 实现"| userService

    userService --> auditPort --> auditLogger --> stdout
    orderService --> auditPort
    middleware --> stdout
```

</details>

实线表示运行时调用；虚线表示 `newServer()` 中的创建、注册和依赖注入。

## 关键依赖边界

- Handler 只负责 HTTP 协议：创建超时为 2 秒的 `context.Context`、解析请求、调用 service、写回 JSON 响应。
- Service 负责参数校验、业务规则和底层错误到 `httpx.AppError` 的转换。
- Service 依赖 repository 接口，而非内存实现；因此可在不改变 service 的情况下替换为数据库实现。
- `order.Service` 通过 `UserFinder` 接口依赖用户查询能力，而不直接访问用户仓储。
- `audit.Logger` 是共享端口，当前由 `AsyncLogger` 通过有界队列和固定 worker 异步写入结构化日志；队列满时最佳努力丢弃，并在关闭窗口内排空已入队事件。

## 订单创建链路

![创建订单的请求时序](images/order-create-sequence.svg)

<details>
<summary>查看可编辑的 Mermaid 源</summary>

```mermaid
sequenceDiagram
    participant C as 客户端
    participant M as 请求日志中间件
    participant H as Order Handler
    participant O as Order Service
    participant U as User Service<br/>(UserFinder)
    participant UR as 用户内存仓储
    participant OR as 订单内存仓储
    participant A as 异步审计日志

    C->>M: POST /orders
    M->>H: 记录 method / path 后转发
    H->>H: DecodeJSON；创建 2 秒 Context
    H->>O: Create(ctx, userId, amount)
    O->>O: 校验 userId、amount
    O->>U: FindByID(ctx, userId)
    U->>UR: FindByID(ctx, id)
    UR-->>U: User / ErrNotFound
    U-->>O: User / error

    alt 用户存在
        O->>OR: Create(ctx, input)
        OR-->>O: Order(status=pending)
        O-)A: 异步入队 order.created
        O-->>H: Order
        H-->>C: 201 JSON
    else 用户不存在或参数无效
        O-->>H: AppError
        H-->>C: 400 JSON
    end
```

</details>

## 路由与能力

| 路由 | 方法 | Handler | Service 能力 |
| --- | --- | --- | --- |
| `/health` | `GET` | `server.go` 中的匿名 handler | 返回服务状态 |
| `/users` | `GET` / `POST` | `user.Handler` | 查询用户列表 / 创建用户 |
| `/users/:id` | `GET` | `user.Handler` | 查询用户详情 |
| `/orders` | `GET` / `POST` | `order.Handler` | 查询订单列表 / 创建订单，并校验用户存在 |
| `/orders/:id` | `GET` | `order.Handler` | 查询订单详情 |

## 运行时与外部依赖

- 模块为 `bridge-go/user-order-api`，要求 Go `1.25.3`。
- `go.mod` 没有第三方模块依赖；HTTP、JSON、日志、并发、上下文与内存数据结构都来自 Go 标准库。
- 用户和订单数据保存在进程内存中，服务重启后会清空；当前没有数据库、缓存、消息队列或外部 HTTP 服务。
