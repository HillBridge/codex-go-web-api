# 阶段 6：认证、授权与基础安全设计

## 目标

为用户订单 API 建立可用于单体生产服务的认证与授权基础：短期 JWT Access Token、高可控的长期服务端会话、订单资源归属、`user`/`admin` 角色、审计、限流、CORS 和 TLS 部署约束。

## 范围与非范围

本阶段实现邮箱密码注册与登录、会话刷新/退出、角色权限、订单归属校验、MySQL 持久化、基础安全中间件和安全回归测试。

本阶段不接入第三方 OIDC、Redis、短信/邮件验证码、MFA、密码找回、设备风险识别、分布式限流或 mTLS。Redis 将在多实例或高请求量时替换本地限流/会话热点读取。

## 认证模型

### 密码与用户

- 新注册请求为 `POST /api/v1/auth/register`，包含 `name`、`email`、`password`。
- 密码 UTF-8 编码后长度为 12–72 字节，使用 `bcrypt` 哈希后写入 `users.password_hash`；响应、日志、审计和错误中永不包含密码或哈希。
- `users.role` 只允许 `user`、`admin`，默认 `user`；`users.auth_version` 默认 1，为后续“退出所有设备/立即使旧 Access Token 失效”预留。
- 现有迁移前创建的用户 `password_hash` 为空，不能登录，且返回统一的 `INVALID_CREDENTIALS`；不自动生成或记录默认密码。
- 管理员首次创建通过启动配置 `BOOTSTRAP_ADMIN_EMAIL` 和 `BOOTSTRAP_ADMIN_PASSWORD` 完成。仅在指定邮箱不存在时创建，密码不写日志；启动配置缺失时不创建管理员。

### 登录与 Token

```text
邮箱 + 密码
   ↓
验证 bcrypt 密码
   ↓
创建 MySQL session（仅保存 Refresh Token 哈希）
   ↓
返回 15 分钟 Access JWT；同时设置 7 天 HttpOnly Refresh Cookie
```

- Access JWT 使用 HS256，签名密钥来自必填 `JWT_SIGNING_KEY`，至少 32 个随机字节；payload 仅含 `sub`（用户 ID）、`role`、`sid`（会话 ID）、`ver`（auth_version）、`iat`、`exp` 和 `iss`。
- Access Token 通过 JSON 响应的 `accessToken` 返回，前端仅保存于内存，并通过 `Authorization: Bearer <token>` 调用受保护接口。
- Refresh Token 使用 `crypto/rand` 随机生成，原文仅以 Cookie 发给浏览器；数据库保存 SHA-256 哈希。Cookie 为 `HttpOnly`、`SameSite=Strict`、路径 `/api/v1/auth`；生产必须启用 `Secure`，本地开发由 `AUTH_COOKIE_SECURE=false` 显式允许 HTTP。
- `POST /api/v1/auth/refresh` 校验 Cookie 的 Token 哈希、会话状态与过期时间，并在一个数据库事务中吊销旧会话、创建新会话、发送新 Cookie 和 Access Token。旧 Refresh Token 重放一律返回 `401 UNAUTHENTICATED`。
- `POST /api/v1/auth/logout` 吊销当前 Refresh Token 并清除 Cookie；同一用户的“退出所有设备”不在本阶段提供公开接口，管理员禁用能力由 `auth_version` 预留。已签发 Access Token 最长 15 分钟后自然失效；当前阶段不在每个请求查询会话或用户记录。

## 数据模型

新增向前迁移：

```text
users
  password_hash VARCHAR(255) NULL
  role VARCHAR(16) NOT NULL DEFAULT 'user'
  auth_version BIGINT NOT NULL DEFAULT 1

sessions
  id CHAR(36) PRIMARY KEY
  user_id BIGINT UNSIGNED NOT NULL
  token_hash CHAR(64) NOT NULL UNIQUE
  expires_at DATETIME(6) NOT NULL
  revoked_at DATETIME(6) NULL
  created_at DATETIME(6) NOT NULL
  last_used_at DATETIME(6) NOT NULL
  INDEX (user_id, revoked_at, expires_at)
```

`sessions.user_id` 外键引用 `users.id` 并禁止删除仍有会话的用户。会话原文、JWT、密码与 Authorization 请求头都不会落库或进入审计字段。

## HTTP 接口

| 接口 | 认证 | 行为 |
| --- | --- | --- |
| `POST /api/v1/auth/register` | 否 | 注册普通用户，返回用户与 Access Token，并设置 Refresh Cookie，`201` |
| `POST /api/v1/auth/login` | 否 | 登录，返回 Access Token，并设置 Refresh Cookie，`200` |
| `POST /api/v1/auth/refresh` | Refresh Cookie | 轮换会话，返回新的 Access Token，`200` |
| `POST /api/v1/auth/logout` | Refresh Cookie | 吊销当前会话、清除 Cookie，`204` |
| `GET /api/v1/auth/me` | Access JWT | 返回当前用户公开资料，`200` |

受保护的已有接口规则：

- `GET /users`：仅 `admin`。
- `GET /users/{id}`：本人或 `admin`。
- `POST /users`：仅 `admin`，不处理密码；推荐客户端改用 `/auth/register` 自助注册。
- `GET /orders`：普通用户仅返回自己的订单，`admin` 返回全部。
- `GET /orders/{id}`、支付、取消：订单所有者或 `admin`。
- 创建订单：必须认证。普通用户省略 `userId` 时由服务端写入当前用户 ID；若提供其他用户 ID 返回 `403 FORBIDDEN`。`admin` 可明确为其他用户创建订单。

错误码：

- 未带、格式不正确、过期、签名无效或已吊销凭证：`401 UNAUTHENTICATED`。
- 已登录但资源不归属或角色不足：`403 FORBIDDEN`。
- 登录/注册失败不泄露账户是否存在：登录统一返回 `401 INVALID_CREDENTIALS`。
- 触发限流：`429 RATE_LIMITED`，带 `Retry-After`。

## 授权中间件与审计

认证中间件解析 JWT，校验签名、issuer、时间和结构化 claims，在 `context.Context` 注入只包含 `UserID`、`Role`、`SessionID` 的 `Principal`。它不在每个业务请求查询会话或用户记录；因此 Access Token 的最长权限滞后窗口固定为 15 分钟。Handler 只从 Principal 获取当前调用者，Service 负责订单所有权和角色规则，Repository 不接收 HTTP 身份信息。

审计记录以下安全事件：`auth.registered`、`auth.logged_in`、`auth.refreshed`、`auth.logged_out`、`auth.denied` 以及管理员越权范围内的资源操作。审计字段仅包含用户 ID、会话 ID、动作、结果、请求 ID 和资源 ID，不含凭证或密码。

## 限流、CORS、TLS 与敏感信息

- 本地单实例实现按“来源 IP + 路由类别”的内存令牌桶。`register` 和 `login` 默认每分钟 5 次；`refresh` 每分钟 20 次；普通 API 每分钟 120 次。仅在明确设置可信代理列表时使用 `X-Forwarded-For`，否则使用 `RemoteAddr`。
- `CORS_ALLOWED_ORIGINS` 是逗号分隔的精确 Origin 白名单；默认拒绝跨域。允许 Origin 时仅允许预定义方法/头（`Authorization`、`Content-Type`、`Idempotency-Key`），并启用 credentials，不使用 `*`。
- 应用本身继续提供 HTTP；生产必须由受信任反向代理终止 TLS，并启用 HSTS、HTTP 到 HTTPS 跳转和 `AUTH_COOKIE_SECURE=true`。README 提供 Nginx/Caddy 部署约束，不在 Go 服务内管理证书。
- 请求日志不记录 Authorization、Cookie 或 JSON 密码字段；所有公开 User JSON 不序列化 `PasswordHash`、`AuthVersion` 或会话信息。

## 配置

新增必填：`JWT_SIGNING_KEY`。

新增可配置项：`JWT_ISSUER`（默认 `user-order-api`）、`ACCESS_TOKEN_TTL`（默认 `15m`）、`REFRESH_TOKEN_TTL`（默认 `168h`）、`AUTH_COOKIE_SECURE`（生产 `true`）、`CORS_ALLOWED_ORIGINS`、`TRUSTED_PROXY_CIDRS`、`RATE_LIMIT_LOGIN_PER_MINUTE`、`RATE_LIMIT_REFRESH_PER_MINUTE`、`RATE_LIMIT_API_PER_MINUTE`、`BOOTSTRAP_ADMIN_EMAIL`、`BOOTSTRAP_ADMIN_PASSWORD`。

## 验证与发布顺序

1. 单元测试：密码规则、JWT 签发/校验、过期/篡改/版本失效、Cookie 属性、限流和 CORS。
2. MySQL 集成测试：会话哈希不可反查、会话轮换只保留一个有效 Token、已吊销 Token 不可刷新、迁移兼容已有订单。
3. HTTP 安全测试：未认证 401、跨用户资源 403、管理员放行、登录错误不枚举账户、重复 Refresh Token 失败、限流 429。
4. 在测试库完成注册→访问→刷新→退出→拒绝访问的完整链路；运行 `go test ./...`、`go vet ./...`、`go test -race ./...`。
5. 上线前在预发布环境使用独立密钥和管理员启动配置验证；备份生产库后执行向前迁移，再部署应用；监控登录失败、429、401/403 和会话刷新错误。
