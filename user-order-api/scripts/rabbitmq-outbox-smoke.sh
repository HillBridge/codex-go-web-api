#!/usr/bin/env bash
set -euo pipefail

base_url="${BASE_URL:-http://localhost:8888}"
echo "[1/4] 检查 Compose 服务健康状态"
docker compose ps

echo "[2/4] 检查 API 与 RabbitMQ"
curl --fail --silent --show-error "$base_url/healthz" >/dev/null
docker compose exec -T rabbitmq rabbitmq-diagnostics -q ping

echo "[3/4] 读取 Outbox/Inbox 投递状态（只读）"
docker compose exec -T mysql mysql -uapp -papp_password user_order_api --batch --skip-column-names -e \
  "SELECT status, COUNT(*) FROM outbox_events GROUP BY status ORDER BY status; SELECT consumer_name, status, COUNT(*) FROM inbox_events GROUP BY consumer_name, status ORDER BY consumer_name, status;"

echo "[4/4] 验证完成"
echo "未执行写入、删库、清空队列或删除数据卷操作；如需产生新事件，请通过 Postman 调用注册/登录/订单接口后重新运行本脚本。"
