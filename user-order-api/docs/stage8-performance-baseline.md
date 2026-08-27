# 阶段 8：API 性能基线

本基线用于决定是否需要 Redis、消息队列或多实例部署。测试脚本只访问 `GET /api/v1/health`，不会创建用户、订单，也不会修改 `user_order_api` 或 `user_order_api_test` 数据库。

## 启动服务

```bash
docker compose up --build -d
docker compose ps
```

## 运行压测

项目使用 Docker 运行 k6，不要求宿主机安装 k6。k6 加入 Compose 内部网络后，通过 `api:8888` 访问 API：

性能基线需要暂时提高 API 限流阈值，否则测试会测到 429 限流而不是服务容量。该值只作用于本地 Compose，不改变代码默认值；测试结束后重新启动 Compose 即可恢复默认的 120 次/分钟。

```bash
RATE_LIMIT_API_PER_MINUTE=10000 docker compose up -d api
```

```bash
docker run --rm \
  --network user-order-api_default \
  -v "$PWD/scripts/load-test.js:/test.js:ro" \
  -e BASE_URL=http://api:8888 \
  -e RATE=50 \
  -e DURATION=30s \
  grafana/k6:0.53.0 run /test.js
```

建议依次测试 `RATE=25`、`RATE=50`、`RATE=100`。如果提高速率后出现大量失败、P95 超过 500ms 或 MySQL 连接池长期接近上限，应先检查 SQL、索引和连接池，再决定是否引入 Redis或扩容 API。

## 记录观察值

压测期间打开 Prometheus `http://localhost:9090`，记录：

```promql
sum(rate(user_order_api_http_requests_total{route="/api/v1/health"}[1m]))
```

```promql
histogram_quantile(0.95, sum(rate(user_order_api_http_request_duration_seconds_bucket{route="/api/v1/health"}[1m])) by (le))
```

```promql
user_order_api_mysql_in_use_connections
```

```promql
user_order_api_mysql_max_open_connections
```

将 k6 输出中的实际请求速率、错误率、P95 延迟，与 Prometheus 的请求速率和连接池数据一起记录。不要只看单次成功率，要在多个速率下重复测试。

## 判定下一步

- API CPU 或内存先达到上限：先增加 API 实例并配置负载均衡；
- MySQL 连接池或慢查询先达到上限：先优化 SQL、索引和连接池；
- 读请求占比高且数据库读压力大：评估 Redis 缓存；
- 审计、通知等非核心任务拖慢请求：评估 MQ + Outbox；
- 没有稳定瓶颈：保持当前单体架构，不提前引入高级组件。

测试完成后可以停止容器：

```bash
docker compose down
```

不要使用 `docker compose down -v`，以免删除本地数据库和监控数据卷。

## 多实例无状态冒烟验证

性能基线之后，可使用项目脚本临时启动第二个 API 实例（宿主机端口 `8889`），验证会话不依赖某一个 API 进程。脚本不会创建账号；请传入数据库中已有的本地测试账号。登录会产生一次正常的 Refresh 会话记录，退出时只清理临时 API 容器。

```bash
MULTI_INSTANCE_EMAIL='已有测试账号邮箱' \
MULTI_INSTANCE_PASSWORD='该账号密码' \
./scripts/multi-instance-smoke.sh
```

脚本验证顺序：

```text
8888 登录 → 8889 /auth/me → 8889 /auth/refresh → 8888 /auth/me
```

如果没有可用测试账号，不要向真实库写入临时账号；可以先在 Postman 中使用已有账号完成登录，再按脚本要求提供相同凭据。

## Redis 共享限流验证

Compose 模式默认启用 Redis，API 通过 `REDIS_ADDR=redis:6379` 使用跨实例共享的固定一分钟窗口；MySQL 仍是用户、订单和 Refresh 会话的事实来源。宿主机直接运行时不设置 `REDIS_ADDR`，则保留进程内存限流。

使用唯一环境名的脚本验证两个 API 实例共用一个限流计数，脚本只访问只读健康接口，不清空 Redis、不删除数据库或数据卷：

```bash
bash scripts/redis-rate-limit-smoke.sh
```

默认限流阈值为 2：主实例和第二实例各成功处理一次请求，第三次请求应返回 `429 RATE_LIMITED`。若 Redis 在运行中不可用，API 返回 `503 RATE_LIMIT_BACKEND_UNAVAILABLE`；启动阶段 Redis Ping 失败则 API 不会监听端口。排查时查看 `docker compose logs api redis`，不要使用 `docker compose down -v`。
