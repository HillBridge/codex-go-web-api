# 阶段 6：认证、授权与基础安全 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 API 提供短期 JWT、可撤销刷新会话、用户/管理员权限、订单归属保护与可配置的基础边界安全能力。

**Architecture:** 认证模块管理密码、Refresh 会话和 Access JWT；认证中间件把 `Principal` 注入请求上下文。用户和订单 Service 根据 Principal 执行归属与角色规则；限流和 CORS 作为 HTTP 外层中间件，MySQL 继续作为唯一持久化来源。

**Tech Stack:** Go 标准库、`database/sql`、MySQL 8、`golang.org/x/crypto/bcrypt`、`github.com/golang-jwt/jwt/v5`、现有 `http.ServeMux`、OpenAPI 3.0、Postman。

**Spec:** `docs/superpowers/specs/2026-08-20-authentication-authorization-security-design.md`

## Global Constraints

- Access JWT 使用 HS256，`JWT_SIGNING_KEY` 必填且至少 32 个随机字节，默认有效期 15 分钟。
- Refresh Token 由 `crypto/rand` 生成，MySQL 仅保存 SHA-256 哈希；默认有效期 7 天，刷新时必须轮换。
- 密码 UTF-8 长度为 12–72 字节，使用 bcrypt；日志、审计、响应和数据库以外的存储不得包含密码、Token、Cookie 或 Authorization 头。
- 默认角色为 `user`；`admin` 可跨用户管理，`user` 只可操作自己拥有的资源。
- CORS 默认拒绝跨域；生产 TLS 由反向代理终止，Cookie 必须 Secure；本地测试库只能使用 `MYSQL_TEST_DSN=user_order_api_test`。

---

### Task 1: 配置、依赖与认证数据迁移（进行中）

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `cmd/api/config.go`, `cmd/api/config_test.go`, `.env.example`
- Create: `internal/platform/database/migrations/0004_users_auth.sql`
- Create: `internal/platform/database/migrations/0005_sessions.sql`
- Modify: `internal/platform/database/database_integration_test.go`

**Interfaces:**
- Produces: `serverConfig` 的 JWT、Cookie、CORS、限流和管理员启动配置。
- Produces: `users.password_hash`, `users.role`, `users.auth_version` 与 `sessions` 表。

- [ ] **Step 1: Write failing configuration and migration tests**

```go
func TestLoadConfigRejectsShortJWTSigningKey(t *testing.T) {
    _, err := loadConfig(testEnvironment(map[string]string{"JWT_SIGNING_KEY": "too-short"}))
    if err == nil { t.Fatal("want JWT signing key validation error") }
}

func TestApplyMigrationsCreatesSessionsTable(t *testing.T) {
    // 迁移后断言 sessions 表、token_hash 唯一约束和 users 三个认证列存在。
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/platform/database -run 'TestLoadConfigRejectsShortJWTSigningKey|TestApplyMigrationsCreatesSessionsTable' -count=1`

Expected: FAIL，因为认证配置和迁移尚不存在。

- [ ] **Step 3: Implement minimal configuration and forward-only migrations**

```sql
ALTER TABLE users
  ADD COLUMN password_hash VARCHAR(255) NULL,
  ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user',
  ADD COLUMN auth_version BIGINT NOT NULL DEFAULT 1;

CREATE TABLE sessions (
  id CHAR(36) PRIMARY KEY,
  user_id BIGINT UNSIGNED NOT NULL,
  token_hash CHAR(64) NOT NULL UNIQUE,
  expires_at DATETIME(6) NOT NULL,
  revoked_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  last_used_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
  INDEX idx_sessions_user_active (user_id, revoked_at, expires_at)
);
```

配置解析 `JWT_SIGNING_KEY`、TTL、Cookie Secure、Origin 白名单、限流上限和管理员启动变量；拒绝非法 duration、负数和过短密钥。

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test -p 1 ./cmd/api ./internal/platform/database -count=1`

Expected: PASS，已有数据库可向前迁移且配置校验严格。

### Task 2: 用户凭据与可撤销会话仓储

**Files:**
- Modify: `internal/user/model.go`, `internal/user/repository.go`, `internal/user/mysql_repository.go`
- Create: `internal/auth/model.go`, `internal/auth/repository.go`, `internal/auth/mysql_repository.go`
- Create: `internal/auth/repository_test.go`, `internal/auth/mysql_repository_integration_test.go`

**Interfaces:**
- Produces: `user.Role`、`user.Credential`、`auth.Session`。
- Produces: `auth.Repository.Create`, `FindActiveByTokenHash`, `Rotate`, `Revoke`。

- [ ] **Step 1: Write failing repository tests**

```go
func TestMemorySessionRepositoryRotatesRefreshTokenOnce(t *testing.T) {
    // 首次 Rotate 返回新会话；再次使用旧哈希返回 ErrSessionNotFound。
}

func TestMySQLSessionRepositoryNeverStoresRawRefreshToken(t *testing.T) {
    // 创建会话后查询 token_hash，断言它等于 SHA-256 而不是原始 Token。
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/auth -run 'TestMemorySessionRepositoryRotatesRefreshTokenOnce' -count=1`

Expected: FAIL，因为 auth 包和会话仓储不存在。

- [ ] **Step 3: Implement minimal repository contracts**

会话只接收/持久化 `tokenHash`；`Rotate` 使用 MySQL 事务，并以 `UPDATE sessions SET revoked_at = UTC_TIMESTAMP(6) WHERE id = ? AND revoked_at IS NULL` 确保旧 Token 只能成功一次。

- [ ] **Step 4: Run repository tests and verify GREEN**

Run: `MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test -p 1 ./internal/auth ./internal/user -count=1`

Expected: PASS，覆盖哈希存储、过期/吊销会话和并发刷新。

### Task 3: 密码、JWT 与认证服务

**Files:**
- Create: `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/auth/jwt.go`, `internal/auth/jwt_test.go`
- Modify: `internal/user/model.go`, `internal/user/mysql_repository.go`
- Modify: `cmd/api/main.go`, `cmd/api/main_test.go`

**Interfaces:**
- Produces: `Service.Register`, `Login`, `Refresh`, `Logout` 和 `Principal{UserID, Role, SessionID}`。
- Produces: `IssueAccessToken(principal)` 与 `ParseAccessToken(raw)`。

- [ ] **Step 1: Write failing security behavior tests**

```go
func TestServiceLoginReturnsSameErrorForMissingUserAndWrongPassword(t *testing.T) {
    // 两种失败均断言 httpx.AppError 为 401 INVALID_CREDENTIALS。
}

func TestParseAccessTokenRejectsExpiredAndTamperedTokens(t *testing.T) {
    // 手工创建过期 token 和修改签名后的 token，均断言错误。
}

func TestBootstrapAdminCreatesOnlyMissingConfiguredAccount(t *testing.T) {
    // 使用内存仓储连续执行两次 bootstrap；断言第一次创建 admin、第二次不重复创建。
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/auth -run 'TestServiceLoginReturnsSameErrorForMissingUserAndWrongPassword|TestParseAccessTokenRejectsExpiredAndTamperedTokens' -count=1`

Expected: FAIL，因为密码服务和 JWT 签发器不存在。

- [ ] **Step 3: Implement the minimal authentication service**

`Register` 用 bcrypt 哈希密码并创建会话；`Login` 对不存在用户也执行固定 bcrypt 比较以减少枚举差异；`Refresh` 轮换会话并签发新 JWT；`Logout` 吊销会话。JWT claims 固定为 `sub`、`role`、`sid`、`ver`、`iat`、`exp`、`iss`，并且只接受 HS256。启动阶段仅在 `BOOTSTRAP_ADMIN_EMAIL` 与 `BOOTSTRAP_ADMIN_PASSWORD` 都存在且目标邮箱不存在时创建 admin；任何密码或 Token 都不记录日志。

- [ ] **Step 4: Run auth tests and verify GREEN**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/auth -count=1`

Expected: PASS，覆盖密码、JWT、刷新轮换和统一登录错误。

### Task 4: 认证 HTTP 接口与 Principal 中间件

**Files:**
- Create: `internal/auth/handler.go`, `internal/auth/middleware.go`, `internal/auth/handler_test.go`
- Modify: `cmd/api/server.go`, `cmd/api/server_test.go`

**Interfaces:**
- Consumes: Task 3 的 `auth.Service`、`Principal`。
- Produces: `/auth/register`、`/auth/login`、`/auth/refresh`、`/auth/logout`、`/auth/me` 和 `auth.PrincipalFromContext`。

- [ ] **Step 1: Write failing HTTP tests**

```go
func TestAuthHTTPFlowSetsRefreshCookieAndProtectsMe(t *testing.T) {
    // register 返回 201 与 accessToken，Set-Cookie 必须为 HttpOnly/SameSite；携带 Bearer 后 /auth/me 为 200，未携带为 401。
}

func TestRefreshTokenCannotBeReused(t *testing.T) {
    // 使用 Cookie refresh 一次为 200，再使用旧 Cookie 必须 401 UNAUTHENTICATED。
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/auth -run 'TestAuthHTTPFlowSetsRefreshCookieAndProtectsMe|TestRefreshTokenCannotBeReused' -count=1`

Expected: FAIL，因为认证路由和中间件尚未注册。

- [ ] **Step 3: Implement handlers and middleware**

公开路由仅为 register/login/refresh/logout/health；`/auth/me` 使用 Bearer middleware。刷新与退出读取指定 Refresh Cookie，分别写入轮换 Cookie 和过期 Cookie。认证失败统一 JSON `401 UNAUTHENTICATED`，不得回显凭证。

- [ ] **Step 4: Run HTTP tests and verify GREEN**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/auth -count=1`

Expected: PASS，Cookie 属性、401、刷新轮换和 Principal 注入均通过。

### Task 5: 用户/订单归属与角色权限

**Files:**
- Modify: `internal/user/service.go`, `internal/user/handler.go`, `internal/user/repository.go`
- Modify: `internal/order/service.go`, `internal/order/handler.go`, `internal/order/repository.go`, `internal/order/mysql_repository.go`
- Modify: `internal/user/*_test.go`, `internal/order/*_test.go`, `cmd/api/server_test.go`

**Interfaces:**
- Consumes: `auth.Principal`。
- Produces: `httpx.ForbiddenCode("FORBIDDEN", ...)` 以及基于当前用户的查询与变更规则。

- [ ] **Step 1: Write failing ownership tests**

```go
func TestOrderOwnerCannotReadOrCancelAnotherUsersOrder(t *testing.T) {
    // user A 请求 user B 的订单详情和 cancel，均断言 403 FORBIDDEN。
}

func TestAdminCanListAllOrdersAndCreateForAnotherUser(t *testing.T) {
    // admin 的订单列表包含所有用户数据，且可明确传 userId 创建订单。
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/order -run 'TestOrderOwnerCannotReadOrCancelAnotherUsersOrder|TestAdminCanListAllOrdersAndCreateForAnotherUser' -count=1`

Expected: FAIL，因为当前请求没有身份和归属校验。

- [ ] **Step 3: Implement authorization rules**

普通用户订单查询自动按 `principal.UserID` 过滤；详情、支付和取消先读取订单再判断归属；管理员绕过归属限制。普通用户创建订单时无 `userId` 则填入当前 ID，有不同 `userId` 返回 403。用户列表仅管理员，用户详情只允许本人或管理员。

- [ ] **Step 4: Run authorization tests and verify GREEN**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./cmd/api ./internal/user ./internal/order -count=1`

Expected: PASS，未认证 401、越权 403、管理员放行和旧订单逻辑兼容。

### Task 6: 限流、CORS、审计脱敏与 TLS 部署约束

**Files:**
- Create: `internal/platform/security/ratelimit.go`, `internal/platform/security/ratelimit_test.go`, `internal/platform/security/cors.go`, `internal/platform/security/cors_test.go`
- Modify: `cmd/api/server.go`, `cmd/api/config.go`, `cmd/api/server_test.go`
- Modify: `internal/platform/httpx/httpx.go`

**Interfaces:**
- Produces: `RATE_LIMITED` 429（含 `Retry-After`）和精确 Origin CORS 响应。

- [ ] **Step 1: Write failing boundary tests**

```go
func TestLoginRateLimitReturnsRetryAfter(t *testing.T) {
    // 第 6 次同 IP 登录在一分钟窗口内返回 429、RATE_LIMITED 和 Retry-After。
}

func TestCORSAllowsOnlyConfiguredOrigin(t *testing.T) {
    // allowlisted Origin 获得精确 Allow-Origin/credentials；其他 Origin 不获得 CORS 响应头。
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/security ./cmd/api -run 'TestLoginRateLimitReturnsRetryAfter|TestCORSAllowsOnlyConfiguredOrigin' -count=1`

Expected: FAIL，因为安全中间件尚不存在。

- [ ] **Step 3: Implement focused middleware**

实现线程安全的内存令牌桶，以 IP 与路由类别构成 key；未配置 trusted proxy 时只信任 `RemoteAddr`。CORS 仅对完全匹配配置的 Origin 写头，预检限制方法和 `Authorization`、`Content-Type`、`Idempotency-Key`。新增 `ForbiddenCode`、`UnauthorizedCode`、`TooManyRequestsCode`，并确保访问日志不输出敏感请求头。

- [ ] **Step 4: Run boundary tests and verify GREEN**

Run: `GOCACHE=/private/tmp/user-order-api-go-cache go test ./internal/platform/security ./cmd/api -count=1`

Expected: PASS，包含并发安全限流、429、CORS 拒绝和允许规则。

### Task 7: 文档、Postman、实机验收与完整回归

**Files:**
- Modify: `docs/openapi.yaml`, `docs/postman/user-order-api.postman_collection.json`, `README.md`, `docs/architecture.md`, `.env.example`

**Interfaces:**
- Consumes: Tasks 1–6 的最终配置和 HTTP 契约。
- Produces: 前端可执行的登录/刷新/订单权限流程，以及生产 TLS 部署和密钥约束文档。

- [ ] **Step 1: 更新契约与示例**

OpenAPI 描述 auth 路由、Bearer 认证、安全错误码、Cookie 刷新与订单归属。Postman 注册 user/admin、保存 Access Token、发送 Bearer、刷新与退出。README 说明随机密钥生成、管理员启动变量、CORS、反向代理 TLS 和 Token 不可写入 localStorage。

- [ ] **Step 2: 在测试库执行完整链路**

在 `user_order_api_test` 启动临时 API，验收：注册 user A/B → A 创建订单 → B 得到 403 → A refresh → logout → access 到期后拒绝。管理员验收跨用户列表和创建。

- [ ] **Step 3: 执行最终验证**

```bash
gofmt -w cmd/api internal/auth internal/order internal/user internal/platform/security
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test -p 1 ./... -count=1
GOCACHE=/private/tmp/user-order-api-go-cache go vet ./...
MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true&charset=utf8mb4&loc=UTC' GOCACHE=/private/tmp/user-order-api-go-cache go test -p 1 -race ./... -count=1
git diff --check
```

Expected: 所有命令以 0 退出；测试/验收过程绝不使用 `user_order_api`。
