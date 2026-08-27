# 阶段 8：Redis 分布式限流设计

## 目标

将当前 API 的登录、Refresh 和普通 API 限流计数从单进程内存迁移到 Redis，使多个 API 实例共享同一限流窗口，同时保留不配置 Redis 时的本地开发能力。

## 范围

本次包含：

- Redis 客户端和连接配置；
- 基于 Redis 原子 Lua 脚本的固定一分钟窗口计数；
- 登录、Refresh、普通 API 三类限流共享计数；
- Compose Redis 服务和健康检查；
- Redis 故障时的明确启动失败或 503 响应；
- 两实例限流验收脚本与文档。

本次不包含：

- 将用户、订单或 Refresh 会话从 MySQL 迁移到 Redis；
- Redis 缓存业务数据；
- 分布式锁、MQ 或微服务拆分；
- 生产 Redis 集群、哨兵和云厂商专属配置。

## 运行模式

| 配置 | 行为 |
| --- | --- |
| `REDIS_ADDR` 为空 | 使用现有进程内存限流，适合单机开发。 |
| `REDIS_ADDR` 已配置 | 启动时 Ping Redis；成功后所有限流请求使用 Redis。 |
| Redis 启动时不可用 | API 启动失败，不伪装成已启用分布式限流。 |
| Redis 运行中不可用 | 限流中间件返回 `503 RATE_LIMIT_BACKEND_UNAVAILABLE`，不退回各实例独立计数。 |

Compose 默认设置 `REDIS_ADDR=redis:6379`；宿主机直接运行未设置该变量时保持原有内存模式。

## 计数算法

使用 Redis Lua 脚本保证 `INCR` 和首次 `EXPIRE` 原子执行：

```lua
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return {current, redis.call('TTL', KEYS[1])}
```

Key 格式为：

```text
user-order-api:<environment>:rate:<class>:<client-ip>
```

不把邮箱、用户 ID、订单 ID、Token 或请求体写入 Key。计数超过限制时返回 `429` 和 `Retry-After`；Redis 错误返回 `503`，响应不暴露 Redis 地址或错误细节。

## 接口边界

限流器依赖抽象计数器，而不是直接依赖 Redis 客户端：

```go
type CounterStore interface {
    Increment(ctx context.Context, key string, window time.Duration) (count int64, ttl time.Duration, err error)
}
```

现有内存计数器实现相同接口；Redis 实现使用 `EVAL`。HTTP 中间件只负责客户端 IP、路由类别、状态码和错误响应，不感知存储实现。

## 验收标准

1. 未配置 `REDIS_ADDR` 时现有单元测试和宿主机启动行为不变。
2. Redis 正常时两个 API 实例共享同一个限流窗口。
3. 低阈值双实例测试中，从不同实例发起的请求总数超过阈值后都返回 `429`。
4. Redis 启动不可用时 API 明确失败；运行中断开时返回 `503 RATE_LIMIT_BACKEND_UNAVAILABLE`。
5. Redis Key 只包含服务、环境、限流类别和客户端 IP，不包含凭证或业务数据。
6. `go test ./...`、`go vet ./...`、`go test -race ./...`、Compose 配置检查全部通过。

