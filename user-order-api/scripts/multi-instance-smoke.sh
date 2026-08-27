#!/usr/bin/env bash
set -euo pipefail

: "${MULTI_INSTANCE_EMAIL:?set MULTI_INSTANCE_EMAIL to an existing local test account}"
: "${MULTI_INSTANCE_PASSWORD:?set MULTI_INSTANCE_PASSWORD to that account's password}"

secondary_container="${MULTI_INSTANCE_CONTAINER:-user-order-api-api-secondary}"
secondary_port="${MULTI_INSTANCE_PORT:-8889}"
mysql_dsn="${MYSQL_DSN:-app:app_password@tcp(mysql:3306)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC}"
jwt_signing_key="${JWT_SIGNING_KEY:-local-development-key-must-be-at-least-32-bytes}"
cookie_file="$(mktemp -t user-order-api-multi-cookie.XXXXXX)"

cleanup() {
	rm -f "$cookie_file"
	docker rm -f "$secondary_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker compose up -d mysql jaeger api >/dev/null
image="$(docker compose images -q api)"
if [ -z "$image" ]; then
	echo "api image was not found; run docker compose up --build -d api first" >&2
	exit 1
fi

docker rm -f "$secondary_container" >/dev/null 2>&1 || true
docker run -d --name "$secondary_container" --network user-order-api_default -p "${secondary_port}:8888" \
	-e MYSQL_DSN="$mysql_dsn" \
	-e JWT_SIGNING_KEY="$jwt_signing_key" \
	-e AUTH_COOKIE_SECURE=false \
	-e OTEL_SERVICE_NAME=user-order-api-secondary \
	-e OTEL_EXPORTER_OTLP_ENDPOINT=jaeger:4317 \
	-e OTEL_EXPORTER_OTLP_INSECURE=true \
	"$image" >/dev/null

for attempt in $(seq 1 30); do
	if curl --fail --silent "http://127.0.0.1:${secondary_port}/healthz" >/dev/null; then
		break
	fi
	if [ "$attempt" -eq 30 ]; then
		echo "secondary API did not become healthy" >&2
		exit 1
	fi
	sleep 1
done

login_payload="$(jq -cn --arg email "$MULTI_INSTANCE_EMAIL" --arg password "$MULTI_INSTANCE_PASSWORD" '{email:$email,password:$password}')"
login_response="$(curl --fail --silent --show-error -c "$cookie_file" -X POST http://127.0.0.1:8888/api/v1/auth/login \
	-H 'Content-Type: application/json' \
	-d "$login_payload")"
access_token="$(printf '%s' "$login_response" | jq -er '.accessToken')"

curl --fail --silent --show-error -H "Authorization: Bearer $access_token" \
	"http://127.0.0.1:${secondary_port}/api/v1/auth/me" >/dev/null

refresh_response="$(curl --fail --silent --show-error -b "$cookie_file" -c "$cookie_file" -X POST "http://127.0.0.1:${secondary_port}/api/v1/auth/refresh")"
rotated_token="$(printf '%s' "$refresh_response" | jq -er '.accessToken')"

curl --fail --silent --show-error -H "Authorization: Bearer $rotated_token" \
	http://127.0.0.1:8888/api/v1/auth/me >/dev/null

echo "multi-instance authentication smoke test passed"
