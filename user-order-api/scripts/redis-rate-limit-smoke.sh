#!/usr/bin/env bash
set -Eeuo pipefail

limit="${REDIS_SMOKE_LIMIT:-2}"
secondary_port="${REDIS_SMOKE_PORT:-8889}"
environment="smoke-$(date +%s)-$$"
secondary_container="${REDIS_SMOKE_CONTAINER:-user-order-api-redis-smoke}"
mysql_dsn="${MYSQL_DSN:-app:app_password@tcp(mysql:3306)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC}"
jwt_signing_key="${JWT_SIGNING_KEY:-local-development-key-must-be-at-least-32-bytes}"

cleanup() {
	docker rm -f "$secondary_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

RATE_LIMIT_API_PER_MINUTE="$limit" REDIS_SMOKE_LIMIT="$limit" REDIS_ENVIRONMENT="$environment" docker compose up --build -d mysql redis jaeger api >/dev/null

primary_container="$(docker compose ps -q api)"
for attempt in $(seq 1 30); do
	status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$primary_container")"
	if [ "$status" = "healthy" ]; then
		break
	fi
	if [ "$status" = "unhealthy" ] || [ "$status" = "exited" ] || [ "$attempt" -eq 30 ]; then
		echo "primary API did not become healthy (status=$status)" >&2
		exit 1
	fi
	sleep 1
done

image="$(docker compose images -q api)"
if [ -z "$image" ]; then
	echo "api image was not found" >&2
	exit 1
fi

docker rm -f "$secondary_container" >/dev/null 2>&1 || true
docker run -d --name "$secondary_container" --network user-order-api_default -p "${secondary_port}:8888" \
	-e MYSQL_DSN="$mysql_dsn" \
	-e JWT_SIGNING_KEY="$jwt_signing_key" \
	-e AUTH_COOKIE_SECURE=false \
	-e TRUSTED_PROXY_CIDRS=127.0.0.0/8 \
	-e REDIS_ADDR=redis:6379 \
	-e REDIS_ENVIRONMENT="$environment" \
	-e RATE_LIMIT_API_PER_MINUTE="$limit" \
	-e OTEL_SERVICE_NAME=user-order-api-redis-smoke \
	-e OTEL_EXPORTER_OTLP_ENDPOINT=jaeger:4317 \
	-e OTEL_EXPORTER_OTLP_INSECURE=true \
	"$image" >/dev/null

sleep 3

request_status() {
	local url="$1"
	local body_file="$2"
	curl --silent --show-error -o "$body_file" -w '%{http_code}' "$url"
}

first_body="$(mktemp -t user-order-api-redis-smoke.XXXXXX)"
second_body="$(mktemp -t user-order-api-redis-smoke.XXXXXX)"
third_body="$(mktemp -t user-order-api-redis-smoke.XXXXXX)"
trap 'rm -f "$first_body" "$second_body" "$third_body"; cleanup' EXIT

first_status="$(request_status http://127.0.0.1:8888/api/v1/health "$first_body")"
second_status="$(request_status "http://127.0.0.1:${secondary_port}/api/v1/health" "$second_body")"
third_status="$(request_status http://127.0.0.1:8888/api/v1/health "$third_body")"

if [ "$first_status" != "200" ] || [ "$second_status" != "200" ] || [ "$third_status" != "429" ]; then
	echo "unexpected statuses: primary=${first_status}, secondary=${second_status}, third=${third_status}" >&2
	echo "third response: $(<"$third_body")" >&2
	exit 1
fi

echo "redis distributed rate-limit smoke test passed"
