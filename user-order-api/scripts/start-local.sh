#!/usr/bin/env bash

set -euo pipefail

project_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$project_root"

docker compose up -d

export MYSQL_DSN="${MYSQL_DSN:-app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC}"

exec go run ./cmd/api
