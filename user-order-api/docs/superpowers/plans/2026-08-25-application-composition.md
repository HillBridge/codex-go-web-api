# Application Composition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move all Auth, User, and Order dependency composition into `internal/app`, leaving `cmd/api` responsible only for process startup and shutdown.

**Architecture:** `internal/app` owns the `Application` HTTP handler, routing, middleware, dependency injection and the two composition factories: `NewMemory` for fast tests and `NewProduction` for a MySQL-backed application. `cmd/api/main.go` opens and migrates MySQL, maps parsed startup configuration to `app.Config`, builds the application, then owns the listener and graceful shutdown lifecycle.

**Tech Stack:** Go 1.25.3, standard library `net/http` and `database/sql`, MySQL 8.4, `github.com/go-sql-driver/mysql`.

**Spec:** [Application composition design](../specs/2026-08-25-application-composition-design.md)

## Global Constraints

- Preserve every public route, HTTP status code, error code, database migration and environment variable.
- `MYSQL_DSN` must continue to select the runtime database; `MYSQL_TEST_DSN` must continue to select only `user_order_api_test` through `testdb.RequireDSN`.
- The normal HTTP test suite must use in-memory repositories and must not require MySQL.
- `Application.Close(ctx)` must continue to drain the async audit logger before the database connection closes.
- Do not introduce a web framework, ORM, dependency-injection library or new runtime dependency.

---

## File structure after the refactor

```text
cmd/api/
  main.go                         startup orchestration only
  config.go                       environment parsing and HTTP server settings
  config_test.go                  configuration and HTTP-server lifecycle tests

internal/app/
  application.go                  Application, routes, middleware, dependency container
  composition.go                  Config, NewMemory and NewProduction
  memory_identity_repository.go   test-only in-memory Auth identity adapter
  application_test.go             in-memory HTTP behavior tests
  production_test.go              MySQL-backed HTTP integration test
```

`internal/auth`、`internal/user`、`internal/order` and `internal/platform` keep their existing responsibilities and public APIs.

### Task 1: Create the application package and move the HTTP composition boundary

**Files:**

- Create: `internal/app/application.go`
- Create: `internal/app/application_test.go`

**Interfaces:**

- Consumes: `user.Repository`, `order.Repository`, `*auth.Service`, `audit.NewAsyncLogger`, `security.CORSMiddleware`, and existing user/order/auth handlers.
- Produces:

```go
package app

type Dependencies struct {
	UserRepository  user.Repository
	OrderRepository order.Repository
	AuthService     *auth.Service
	CookieSecure    bool
	CORSOrigins     []string
	TrustedProxies  []netip.Prefix
	RateLimits      security.Limits
}

type Application struct {
	handler     http.Handler
	auditLogger *audit.AsyncLogger
}

func NewWithDependencies(logger *slog.Logger, deps Dependencies) *Application
func (a *Application) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (a *Application) Close(ctx context.Context) error
```

- `NewWithDependencies` must reject neither repositories nor services; tests always provide valid dependencies. Its only job is to construct the same route tree and middleware chain currently created by `newApplicationWithSecurity`.

- [x] **Step 1: Add a failing application-package test for injected repositories**

Create `internal/app/application_test.go` in package `app`. Copy the current `TestApplicationUsesProvidedRepositories` into this package and adapt its construction to the new API. Add small package-local HTTP helpers (`postJSON`, `decodeBody`, and `registerAccessToken`) needed only by this new test; do not remove helpers from `cmd/api/server_test.go` until Task 3 moves the full HTTP test suite.

For this boundary test only, copy the current `memoryIdentityRepository` type and its `CreateIdentity`, `FindIdentityByEmail`, and `FindIdentityByID` methods from `cmd/api/server.go` into the test file as `testMemoryIdentityRepository`. Build the Auth service with that test adapter so `/auth/register` writes through the injected `userRepo`:

```go
authService := auth.NewService(
	newTestMemoryIdentityRepository(userRepo),
	auth.NewMemoryRepository(),
	auth.NewTokenManager([]byte("test-signing-key-that-is-at-least-32-bytes"), "user-order-api", 15*time.Minute, time.Now),
	time.Hour,
	time.Now,
)
```

Task 2 replaces this test-local adapter with the production `internal/app` memory adapter.

```go
app := NewWithDependencies(logger, Dependencies{
	UserRepository:  userRepo,
	OrderRepository: order.NewMemoryRepository(),
	AuthService:     authService,
	CookieSecure:    false,
	RateLimits:      security.Limits{LoginPerMinute: 5, RefreshPerMinute: 20, APIPerMinute: 120},
})
```

Keep its assertion that registration writes into the injected `userRepo`; this proves the route → handler → service chain receives the supplied repository rather than creating an implicit one.

- [x] **Step 2: Run the new test and verify it fails because `app` does not exist**

Run:

```bash
go test ./internal/app -run '^TestApplicationUsesProvidedRepositories$'
```

Expected: compilation failure reporting that package `internal/app` or `NewWithDependencies` is missing.

- [x] **Step 3: Implement `Application` and `NewWithDependencies`**

Move the following code from `cmd/api/server.go` into `internal/app/application.go`, changing package name from `main` to `app`:

- `application` renamed to exported `Application`;
- `newApplicationWithSecurity` replaced by `NewWithDependencies` and values read from `Dependencies`;
- `routeNotFound`, `requestLogMiddleware`, `requestIDMiddleware`, `recoveryMiddleware`, `statusRecorder`, `requestIDFromContext`, and `newRequestID`;
- route registration, including the `/api/v1` prefix, Bearer middleware and CORS/rate-limit middleware.

The construction body must preserve this ordering:

```go
auditLogger := audit.NewAsyncLogger(logger)
deps.AuthService.SetAuditLogger(auditLogger)

userHandler := user.NewHandler(user.NewService(deps.UserRepository, auditLogger))
orderHandler := order.NewHandler(order.NewService(deps.OrderRepository, deps.UserRepository, auditLogger))
authHandler := auth.NewHandler(deps.AuthService, deps.CookieSecure)
```

Then return `&Application{handler: requestIDMiddleware(requestLogMiddleware(logger, recoveryMiddleware(logger, secured))), auditLogger: auditLogger}`. Keep `ServeHTTP` as a direct delegation and `Close` as `a.auditLogger.Close(ctx)`.

- [x] **Step 4: Make the moved test pass and run package-local HTTP tests**

Run:

```bash
go test ./internal/app -run '^TestApplicationUsesProvidedRepositories$'
```

Expected: PASS. The test must prove the injected user repository contains the registered user after the HTTP request.

- [x] **Step 5: Run regression tests and commit the isolated boundary move**

Run:

```bash
go test ./internal/app ./cmd/api
```

Expected: PASS after all remaining `cmd/api` references compile against the new package.

Commit:

```bash
git add internal/app
git commit -m "refactor: introduce application composition package"
```

### Task 2: Add explicit memory and MySQL composition factories

**Files:**

- Create: `internal/app/composition.go`
- Create: `internal/app/memory_identity_repository.go`
- Modify: `internal/app/application_test.go`
- Create: `internal/app/production_test.go`

**Interfaces:**

- Consumes: `*sql.DB`, the existing three MySQL repositories, existing in-memory repositories, `auth.NewTokenManager`, `auth.NewService`, and `security.Limits`.
- Produces:

```go
type Config struct {
	JWTSigningKey          string
	JWTIssuer              string
	AccessTokenTTL         time.Duration
	RefreshTokenTTL        time.Duration
	AuthCookieSecure       bool
	CORSAllowedOrigins     []string
	TrustedProxyCIDRs      []netip.Prefix
	LoginRateLimitPerMinute   int
	RefreshRateLimitPerMinute int
	APIRateLimitPerMinute     int
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
}

func NewMemory(ctx context.Context, logger *slog.Logger, config Config) (*Application, error)
func NewProduction(ctx context.Context, db *sql.DB, logger *slog.Logger, config Config) (*Application, error)
```

- `NewMemory` must use one shared `user.MemoryRepository`. Its Auth identity adapter must call `users.Create` and preserve the resulting user ID, so registration followed by order creation finds the same user.
- `NewProduction` must use one `auth.MySQLRepository` value for both Auth interfaces, plus `user.NewMySQLRepository(db)` and `order.NewMySQLRepository(db)`.

- [ ] **Step 1: Add failing factory tests**

In `application_test.go`, add `TestNewMemoryRegistersThenCreatesOrder`. Construct the application with a deterministic test config, register via `/api/v1/auth/register`, obtain the access token, then call `POST /api/v1/orders` and assert `201 Created` and `status: "pending"`.

Use this shared configuration helper in package `app`:

```go
func testConfig() Config {
	return Config{
		JWTSigningKey:    "test-signing-key-that-is-at-least-32-bytes",
		JWTIssuer:        "user-order-api",
		AccessTokenTTL:   15 * time.Minute,
		RefreshTokenTTL:  time.Hour,
		AuthCookieSecure: false,
		LoginRateLimitPerMinute:   5,
		RefreshRateLimitPerMinute: 20,
		APIRateLimitPerMinute:     120,
	}
}
```

In `production_test.go`, move `TestMySQLAuthenticationAndOrderHTTPFlow` from `cmd/api/server_test.go`. It must call `testdb.RequireDSN(t, os.Getenv("MYSQL_TEST_DSN"))`, open and migrate the database, build via `NewProduction`, and clean up the created session, order and user. This test must not construct repositories directly.

- [ ] **Step 2: Verify both factory tests fail before implementation**

Run:

```bash
go test ./internal/app -run '^TestNewMemoryRegistersThenCreatesOrder$'
MYSQL_TEST_DSN="$MYSQL_TEST_DSN" go test ./internal/app -run '^TestMySQLAuthenticationAndOrderHTTPFlow$'
```

Expected: the first fails because `NewMemory` is absent. The second either fails for the missing `NewProduction` symbol or skips if `MYSQL_TEST_DSN` is unset.

- [ ] **Step 3: Implement memory identity storage and shared Auth construction**

In `memory_identity_repository.go`, move `memoryIdentityRepository` and its three methods from `cmd/api/server.go` into package `app`. Keep the existing behavior that maps `user.ErrEmailTaken` to `auth.ErrEmailTaken` and normalizes lookup email.

In `composition.go`, implement an unexported shared constructor:

```go
func newAuthService(identities auth.IdentityRepository, sessions auth.Repository, config Config) *auth.Service {
	return auth.NewService(
		identities,
		sessions,
		auth.NewTokenManager([]byte(config.JWTSigningKey), config.JWTIssuer, config.AccessTokenTTL, time.Now),
		config.RefreshTokenTTL,
		time.Now,
	)
}
```

`NewMemory` creates `userRepo := user.NewMemoryRepository()`, passes `newMemoryIdentityRepository(userRepo)` and `auth.NewMemoryRepository()` to `newAuthService`, then calls `NewWithDependencies` with the memory user and order repositories. Both factories map the three rate-limit fields into `security.Limits{LoginPerMinute: config.LoginRateLimitPerMinute, RefreshPerMinute: config.RefreshRateLimitPerMinute, APIPerMinute: config.APIRateLimitPerMinute}` before constructing `Dependencies`.

`NewProduction` creates `authRepo := auth.NewMySQLRepository(db)`, passes it twice to `newAuthService`, and calls `NewWithDependencies` with the three MySQL repositories. It must call `BootstrapAdmin` only when `config.BootstrapAdminEmail != ""`; wrap failure with `fmt.Errorf("bootstrap admin: %w", err)` and log `"bootstrap admin account created"` only when it returns `created == true`.

- [ ] **Step 4: Verify the memory factory and MySQL factory**

Run:

```bash
go test ./internal/app -run '^TestNewMemoryRegistersThenCreatesOrder$'
MYSQL_TEST_DSN="$MYSQL_TEST_DSN" go test ./internal/app -run '^TestMySQLAuthenticationAndOrderHTTPFlow$'
```

Expected: the memory test passes without MySQL. With a valid dedicated test DSN, the MySQL HTTP flow passes; without it, the MySQL test reports SKIP from `testdb.RequireDSN`.

- [ ] **Step 5: Commit factory composition**

```bash
git add internal/app
git commit -m "refactor: centralize memory and mysql app composition"
```

### Task 3: Make `cmd/api` a lifecycle-only entry point and relocate its tests

**Files:**

- Modify: `cmd/api/main.go`
- Delete: `cmd/api/server.go`
- Delete: `cmd/api/server_test.go`
- Modify: `internal/app/application_test.go`
- Modify: `internal/app/production_test.go`
- Modify: `cmd/api/config_test.go`

**Interfaces:**

- Consumes: `app.NewProduction(ctx, db, logger, app.Config)` and the unchanged `newHTTPServer`, `serveUntilCancelled`, `loadConfig` functions in package `main`.
- Produces: a `main` package that has no imports of `auth`, `order`, `audit`, `httpx`, `security`, or `user`.

- [ ] **Step 1: Move all remaining HTTP behavior tests before deleting the command-level server file**

Move these tests and their helper functions from `cmd/api/server_test.go` into `internal/app/application_test.go`: user/order flow, idempotency, lifecycle, authorization, middleware and close-draining behavior. Keep tests in package `app` so they may construct `Application` directly when a dedicated dependency case requires it.

Move only `TestMySQLAuthenticationAndOrderHTTPFlow` into `internal/app/production_test.go`; it remains the sole HTTP test that reads `MYSQL_TEST_DSN`.

Replace every `newTestServer(t)` helper body with:

```go
application, err := NewMemory(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), testConfig())
if err != nil {
	t.Fatal(err)
}
t.Cleanup(func() { _ = application.Close(context.Background()) })
return application
```

For the bootstrap-admin helper, copy `testConfig()`, set `BootstrapAdminEmail` and `BootstrapAdminPassword`, call `NewMemory`, then log in through the existing HTTP helper. Do not reach into `auth.Service` from the test.

- [ ] **Step 2: Run the moved HTTP tests and verify the command package no longer owns application tests**

Run:

```bash
go test ./internal/app
go test ./cmd/api
```

Expected: `internal/app` passes all HTTP behavior tests using memory; `cmd/api` runs only configuration and HTTP-server lifecycle tests.

- [ ] **Step 3: Replace production composition in `main.go`**

Remove imports for `auth`, `order`, `security` and `user`. After MySQL migration, construct an `app.Config` directly from `serverConfig`:

```go
application, err := app.NewProduction(startupCtx, db, logger, app.Config{
	JWTSigningKey:          config.JWTSigningKey,
	JWTIssuer:              config.JWTIssuer,
	AccessTokenTTL:         config.AccessTokenTTL,
	RefreshTokenTTL:        config.RefreshTokenTTL,
	AuthCookieSecure:       config.AuthCookieSecure,
	CORSAllowedOrigins:     config.CORSAllowedOrigins,
	TrustedProxyCIDRs:      config.TrustedProxyCIDRs,
	LoginRateLimitPerMinute:   config.LoginRateLimitPerMinute,
	RefreshRateLimitPerMinute: config.RefreshRateLimitPerMinute,
	APIRateLimitPerMinute:     config.APIRateLimitPerMinute,
	BootstrapAdminEmail:    config.BootstrapAdminEmail,
	BootstrapAdminPassword: config.BootstrapAdminPassword,
})
if err != nil {
	log.Fatal(err)
}
```

Keep opening/migrating `db`, calling `newHTTPServer`, handling operating-system signals and calling `application.Close` unchanged. `main.go` imports no domain, middleware, audit, or security package after this step.

- [ ] **Step 4: Delete the obsolete command composition file**

Delete `cmd/api/server.go` once no production or test source refers to `newServer`, `newApplication`, `memoryIdentityRepository`, middleware helpers or the old `application` type. `cmd/api/config.go` continues to own `newHTTPServer` and `serveUntilCancelled`, so its existing lifecycle tests remain valid.

- [ ] **Step 5: Verify the complete refactor**

Run:

```bash
go test ./...
go vet ./...
go test -race ./...
```

Then, with the local MySQL service running and `MYSQL_DSN` loaded from the local environment:

```bash
go run ./cmd/api
```

Expected: the service starts, `/health` is available, and registration followed by authenticated order creation succeeds. When `MYSQL_TEST_DSN` is absent, only MySQL integration tests skip; ordinary tests still pass.

- [ ] **Step 6: Commit the entry-point simplification**

```bash
git add cmd/api internal/app
git commit -m "refactor: isolate api startup from application wiring"
```

## Plan self-review

- Spec coverage: Task 1 moves the application boundary; Task 2 supplies the shared memory/MySQL composition and preserves all three runtime paths; Task 3 reduces `main.go` to lifecycle management and moves HTTP tests to the package owning the application.
- Placeholder scan: no incomplete requirements, deferred work, or unspecified interfaces remain.
- Type consistency: `Application`, `Dependencies`, `Config`, `NewWithDependencies`, `NewMemory`, and `NewProduction` are introduced before later tasks use them. The MySQL test uses `NewProduction`, not direct repository construction.
