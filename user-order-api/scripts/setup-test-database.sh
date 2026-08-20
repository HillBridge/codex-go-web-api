#!/usr/bin/env bash

set -euo pipefail

project_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$project_root"

docker compose up -d --wait --wait-timeout 60
docker compose exec -T mysql mysql -uroot -proot_password -e "CREATE DATABASE IF NOT EXISTS user_order_api_test; GRANT ALL PRIVILEGES ON user_order_api_test.* TO 'app'@'%';"
