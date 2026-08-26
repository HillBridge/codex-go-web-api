# 阶段 6：认证、授权与基础安全

本文说明 `user-order-api` 的认证、会话、权限与基础安全机制，以及前后端接入时需要遵循的约定。

## 目标与范围

项目使用“短期 JWT Access Token + 长期服务端 Refresh Session”的组合：既能让业务接口快速完成身份校验，也能让服务端主动撤销会话。

本阶段包含邮箱密码注册与登录、会话刷新和退出、`user`/`admin` 角色、订单资源归属校验、MySQL 持久化、审计、限流、CORS 与 TLS 部署约束。

不包含第三方 OIDC、Redis、短信/邮件验证码、MFA、密码找回、设备风险识别、分布式限流和 mTLS。这些能力应在多实例、高流量或明确业务需求出现后再引入。

## 注册、登录与 Token

```text
邮箱 + 密码
   ↓
验证 bcrypt 密码哈希
   ↓
创建 MySQL session（仅保存 Refresh Token 哈希）
   ↓
JSON 返回 15 分钟 Access Token
同时设置 7 天 HttpOnly Refresh Cookie
```

### 密码与用户

- 注册接口为 `POST /api/v1/auth/register`，包含 `name`、`email`、`password`。
- 密码 UTF-8 编码后必须为 12–72 字节；服务端用 `bcrypt` 生成哈希，只保存到 `users.password_hash`。
- 密码原文和密码哈希不会出现在 API 响应、日志、审计记录或错误信息中。
- 用户角色只有 `user`、`admin`；新注册用户默认是 `user`。
- `auth_version` 当前默认值为 1，为未来“退出所有设备”或强制旧 Token 失效预留。
- 旧迁移前创建且没有 `password_hash` 的用户不能登录，统一返回 `INVALID_CREDENTIALS`，系统不会生成默认密码。
- 管理员可通过启动配置 `BOOTSTRAP_ADMIN_EMAIL`、`BOOTSTRAP_ADMIN_PASSWORD` 创建；仅在对应邮箱不存在时创建，且密码不会写入日志。

### Access Token

- Access Token 是 HS256 签名的 JWT，签名密钥来自必填环境变量 `JWT_SIGNING_KEY`，至少 32 字节。
- JWT 载荷只包含用户 ID（`sub`）、角色（`role`）、会话 ID（`sid`）、认证版本（`ver`）、签发时间（`iat`）、过期时间（`exp`）和签发者（`iss`）。
- 默认有效期为 15 分钟；通过 JSON 字段 `accessToken` 返回。
- 前端只应将 Access Token 保存于 JavaScript 内存，并通过 `Authorization: Bearer <token>` 调用受保护接口。
- JWT 的 Header 和 Payload 可解码但不可伪造；服务端必须验证 HS256 签名、签发者、时间与会话状态。

### Refresh Token

- Refresh Token 使用 `crypto/rand` 随机生成。
- 原文只以 `refresh_token` Cookie 发给浏览器；数据库只保存其 SHA-256 哈希到 `sessions.token_hash`。
- Cookie 使用 `HttpOnly`、`SameSite=Strict`，路径为 `/api/v1/auth`；生产环境必须开启 `Secure`。
- 本地 HTTP 开发时，才显式设置 `AUTH_COOKIE_SECURE=false`。
- `POST /api/v1/auth/refresh` 会校验 Cookie、会话状态和过期时间，并在一个事务中吊销旧会话、创建新会话、写入新 Cookie 和返回新 Access Token。
- 旧 Refresh Token 被再次使用时，返回 `401 UNAUTHENTICATED`。
- `POST /api/v1/auth/logout` 会撤销当前 Refresh Session 并清除 Cookie；该会话对应的 Access Token 在下一次受保护请求时也会失效。

## 会话数据

```text
sessions
  id           会话 ID
  user_id      所属用户
  token_hash   Refresh Token 的 SHA-256 哈希
  expires_at   自然过期时间
  revoked_at   撤销时间；NULL 代表尚未撤销
  created_at
  last_used_at
```

会话有效的条件是：`revoked_at IS NULL` 且 `expires_at` 仍在未来。撤销不会修改 `expires_at`，因此被撤销会话仍保留原定到期时间，方便审计和排障。

数据库不保存密码原文、Refresh Token 原文、Access Token 原文或 Authorization 请求头。

## 认证接口

| 接口 | 认证要求 | 行为 |
| --- | --- | --- |
| `POST /api/v1/auth/register` | 无 | 注册普通用户，返回用户与 Access Token，设置 Refresh Cookie，`201` |
| `POST /api/v1/auth/login` | 无 | 使用邮箱密码登录，返回 Access Token，设置 Refresh Cookie，`200` |
| `POST /api/v1/auth/refresh` | Refresh Cookie | 轮换会话，返回新 Access Token，`200` |
| `POST /api/v1/auth/logout` | Refresh Cookie | 撤销当前会话、清除 Cookie，`204` |
| `GET /api/v1/auth/me` | Access Token | 返回当前用户公开资料，`200` |

登录失败不会告诉客户端“邮箱不存在”还是“密码错误”，统一返回：

```json
{"code":"INVALID_CREDENTIALS","error":"invalid credentials"}
```

## 授权规则

认证中间件会验证 JWT 和对应 session，通过后只在 `context.Context` 中放入 `UserID`、`Role`、`SessionID`。Handler 不信任客户端传入的用户身份；Service 负责角色和资源归属判断。

| 资源操作 | 普通用户 `user` | 管理员 `admin` |
| --- | --- | --- |
| 查询用户列表 | 不允许 | 允许 |
| 查询用户详情 | 只能查询自己 | 允许查询任何用户 |
| 创建普通用户（`/users`） | 不允许 | 允许，但不设置密码 |
| 创建订单 | 只能为自己创建 | 可指定任意用户 |
| 查询订单列表 | 只能看到自己的订单 | 可查看全部 |
| 查询、支付、取消订单 | 只能操作自己的订单 | 可操作任意订单 |

普通用户创建订单时，如果省略 `userId`，服务端自动使用当前登录用户；若传入其他用户 ID，返回 `403 FORBIDDEN`。

## 错误码

| 情况 | HTTP 状态 | 错误码 |
| --- | --- | --- |
| 未携带、格式错误、过期、签名无效或已撤销的凭证 | `401` | `UNAUTHENTICATED` |
| 登录邮箱或密码不匹配 | `401` | `INVALID_CREDENTIALS` |
| 已登录但无资源权限或角色不足 | `403` | `FORBIDDEN` |
| 登录、注册或刷新频率过高 | `429` | `RATE_LIMITED` |

## 审计与基础安全

- 审计会记录注册、登录、刷新、退出、认证拒绝和管理员范围内的资源操作。
- 审计字段只包含用户 ID、会话 ID、动作、结果、请求 ID 和资源 ID；不包含凭证、密码或密码哈希。
- 限流按“来源 IP + 路由类别”执行：注册/登录默认每分钟 5 次，刷新默认每分钟 20 次，普通 API 默认每分钟 120 次。
- 只有配置了可信代理 CIDR 时，才使用 `X-Forwarded-For`；否则使用连接来源地址。
- `CORS_ALLOWED_ORIGINS` 是精确 Origin 白名单，默认拒绝跨域；不会使用 `*` 与 credentials 的危险组合。
- 请求日志不记录 `Authorization`、Cookie 或 JSON 密码字段。

## 生产部署要求

- 生产环境由受信任反向代理或负载均衡器终止 TLS，并启用 HTTPS、HSTS 与 HTTP→HTTPS 跳转。
- 生产必须设置独立、随机的 `JWT_SIGNING_KEY`，不能使用本地开发密钥。
- 生产必须设置 `AUTH_COOKIE_SECURE=true`。
- 在预发布环境使用独立密钥和管理员配置完成验证；备份生产数据库后执行向前迁移，再部署应用。
- 监控登录失败、429、401/403 与 Refresh 失败率，及时发现攻击、配置或会话问题。

## 关键配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `JWT_SIGNING_KEY` | 无，必填 | HS256 签名密钥，至少 32 字节。 |
| `JWT_ISSUER` | `user-order-api` | JWT 签发者。 |
| `ACCESS_TOKEN_TTL` | `15m` | Access Token 有效期。 |
| `REFRESH_TOKEN_TTL` | `168h` | Refresh Session 有效期。 |
| `AUTH_COOKIE_SECURE` | `true` | 生产必须为 `true`。 |
| `CORS_ALLOWED_ORIGINS` | 空 | 允许跨域的前端 Origin 白名单。 |
| `TRUSTED_PROXY_CIDRS` | 空 | 可信反向代理 CIDR 列表。 |
| `RATE_LIMIT_LOGIN_PER_MINUTE` | `5` | 注册和登录的单 IP 每分钟上限。 |
| `RATE_LIMIT_REFRESH_PER_MINUTE` | `20` | 刷新会话的单 IP 每分钟上限。 |
| `RATE_LIMIT_API_PER_MINUTE` | `120` | 普通 API 的单 IP 每分钟上限。 |
| `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` | 空 | 同时设置时创建首个管理员。 |

## 验证清单

1. 注册、登录、获取当前用户。
2. 使用 Refresh Cookie 刷新 Access Token；再次使用旧 Cookie 应得到 `401`。
3. 退出登录后，原 Access Token 访问受保护接口应得到 `401`。
4. 普通用户访问其他用户订单应得到 `403`；管理员操作应通过。
5. 验证错误密码、过期 Token、篡改 Token 和限流场景。
6. 在专用 `user_order_api_test` 数据库执行 `go test ./...`、`go vet ./...`、`go test -race ./...`。
