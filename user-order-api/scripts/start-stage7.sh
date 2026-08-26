#!/usr/bin/env bash

set -euo pipefail

project_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$project_root"

docker compose up -d mysql
./scripts/setup-test-database.sh

export MYSQL_DSN="${MYSQL_DSN:-app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC}"
export AUTH_COOKIE_SECURE="${AUTH_COOKIE_SECURE:-false}"
export BOOTSTRAP_ADMIN_EMAIL="${BOOTSTRAP_ADMIN_EMAIL:-stage7-admin@example.com}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-correct-password}"

if [[ -z "${JWT_SIGNING_KEY:-}" ]]; then
	JWT_SIGNING_KEY=$(openssl rand -hex 32)
	export JWT_SIGNING_KEY
	echo "JWT_SIGNING_KEY was not set; generated a temporary key for Stage 7 verification."
fi

exec go run ./cmd/api
