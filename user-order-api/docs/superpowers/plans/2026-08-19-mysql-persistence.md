# MySQL Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the API on MySQL with forward-only embedded migrations and cursor-paginated user and order lists.

**Architecture:** `cmd/api/main.go` loads a required `MYSQL_DSN`, opens and migrates one MySQL connection pool, then injects MySQL repositories into the existing service layer. Repository interfaces gain a neutral generic page contract; handlers parse `limit` and `afterId` and return `{items, nextCursor}`. In-memory repositories remain test fixtures only.

**Tech Stack:** Go 1.25.3, `database/sql`, `github.com/go-sql-driver/mysql`, MySQL 8.4, Docker Compose.

**Spec:** `docs/superpowers/specs/2026-08-19-mysql-persistence-design.md`

## Global Constraints

- Add only `github.com/go-sql-driver/mysql`; do not add an ORM or migration framework.
- Require `MYSQL_DSN` for production startup; do not silently fall back to memory storage.
- MySQL DDL migrations are forward-only and one DDL statement per embedded migration file.
- Keep successful create and find-by-ID API responses unchanged.
- List endpoints intentionally become `{items, nextCursor}` with `limit` 1–100 and `afterId`.
- Start every behavior change with a failing test and run all validation with an isolated Go build cache.
- Do not create a commit unless the user explicitly requests one.

---

### Task 1: MySQL configuration and local development environment

**Files:**
- Create: `compose.yaml`
- Create: `.env.example`
- Modify: `.gitignore`
- Modify: `go.mod`
- Modify: `cmd/api/config.go`
- Modify: `cmd/api/config_test.go`
- Modify: `README.md`

**Interfaces:**
- `serverConfig` gains `MySQLDSN string`.
- `loadConfig(getenv)` returns an error with message `MYSQL_DSN is required` when the value is blank.

- [ ] **Step 1: Write failing configuration tests**

  Add a test whose environment contains `MYSQL_DSN=app:secret@tcp(localhost:3307)/user_order_api?parseTime=true&loc=UTC` and asserts that `config.MySQLDSN` preserves it. Add a separate test with no `MYSQL_DSN` that expects the exact required-value error.

- [ ] **Step 2: Verify the tests fail because the config field and validation do not exist**

  Run: `env GOCACHE=<temporary writable directory> go test ./cmd/api -run TestLoadConfig`

  Expected: compilation failure mentioning `MySQLDSN`, followed by failure until the missing-DSN branch is implemented.

- [ ] **Step 3: Implement configuration and local MySQL files**

  Add `MYSQL_DSN` validation after the existing HTTP timeout parsing. Create `compose.yaml` with one `mysql:8.4` service, `3307:3306`, database `user_order_api`, application user `app`, password `app_password`, root password `root_password`, healthcheck `mysqladmin ping`, and named volume `mysql_data`. Add `.env` to `.gitignore`, put the Compose-compatible DSN in `.env.example`, add the MySQL driver with `go get github.com/go-sql-driver/mysql`, and document `docker compose up -d` plus `MYSQL_DSN` in README.

- [ ] **Step 4: Verify Task 1**

  Run: `env GOCACHE=<temporary writable directory> go test ./cmd/api -run TestLoadConfig`

  Expected: all configuration tests pass.

### Task 2: Connection pool and embedded forward-only migrations

**Files:**
- Create: `internal/platform/database/mysql.go`
- Create: `internal/platform/database/migrate.go`
- Create: `internal/platform/database/migrations/0001_users.sql`
- Create: `internal/platform/database/migrations/0002_orders.sql`
- Create: `internal/platform/database/database_integration_test.go`
- Modify: `cmd/api/main.go`
- Modify: `go.mod`

**Interfaces:**
- `database.Open(ctx context.Context, dsn string) (*sql.DB, error)` opens, configures, and pings MySQL.
- `database.ApplyMigrations(ctx context.Context, db *sql.DB) error` creates `schema_migrations` and applies unrecorded embedded migration files.
- Integration tests use `MYSQL_TEST_DSN`; when unset they call `t.Skip`.

- [ ] **Step 1: Write failing migration integration tests**

  When `MYSQL_TEST_DSN` is set, open a fresh test database, call `ApplyMigrations` twice, and assert `users`, `orders`, and two migration records exist after both calls. Add a second test that attempts an order with a nonexistent `user_id` and expects MySQL error number `1452`.

- [ ] **Step 2: Verify the integration tests fail for missing database package**

  Run: `MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<temporary writable directory> go test ./internal/platform/database -run TestApplyMigrations`

  Expected: compilation failure mentioning `database.Open` or `database.ApplyMigrations` before implementation.

- [ ] **Step 3: Implement the pool, migration runner, schema, and lifecycle wiring**

  In `Open`, set max open connections to `10`, max idle connections to `5`, max connection lifetime to `30m`, and use a 5-second `PingContext`. Embed and lexically sort `migrations/*.sql`; create `schema_migrations(version VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL)` before reading versions. For each unrecorded file, execute its single DDL statement and then insert its filename in `schema_migrations`.

  `0001_users.sql` creates `users(id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, name VARCHAR(120) NOT NULL, email VARCHAR(254) NOT NULL UNIQUE, created_at DATETIME(6) NOT NULL)`. `0002_orders.sql` creates `orders(id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, user_id BIGINT UNSIGNED NOT NULL, amount BIGINT NOT NULL CHECK (amount > 0), status VARCHAR(16) NOT NULL, created_at DATETIME(6) NOT NULL, CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT, INDEX idx_orders_user_id (user_id, id))`.

  In `main`, open and migrate MySQL before constructing the application. Defer `db.Close()` before registering the application-close defer so shutdown order is HTTP → audit → database.

- [ ] **Step 4: Verify Task 2**

  Run: `docker compose up -d && MYSQL_TEST_DSN='app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC' env GOCACHE=<temporary writable directory> go test ./internal/platform/database -run TestApplyMigrations`

  Expected: migration is idempotent and foreign-key enforcement is confirmed.

### Task 3: MySQL repositories and production dependency injection

**Files:**
- Create: `internal/user/mysql_repository.go`
- Create: `internal/order/mysql_repository.go`
- Create: `internal/user/mysql_repository_integration_test.go`
- Create: `internal/order/mysql_repository_integration_test.go`
- Modify: `internal/order/repository.go`
- Modify: `internal/order/service.go`
- Modify: `cmd/api/server.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- `user.NewMySQLRepository(db *sql.DB) user.Repository`
- `order.NewMySQLRepository(db *sql.DB) order.Repository`
- `newApplication(logger *slog.Logger, userRepo user.Repository, orderRepo order.Repository) *application`
- MySQL duplicate-key error `1062` maps to `user.ErrEmailTaken`; foreign-key error `1452` maps to `order.ErrUserNotFound`.

- [ ] **Step 1: Write failing repository integration tests**

  Use a transaction or isolated database rows to assert that creating a user lowercases the email, duplicate email returns `user.ErrEmailTaken`, `FindByID` maps no rows to each domain’s `ErrNotFound`, creating an order persists `pending`, and creating an order for an absent user returns `order.ErrUserNotFound`.

- [ ] **Step 2: Verify focused repository tests fail**

  Run: `MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<temporary writable directory> go test ./internal/user ./internal/order -run MySQL`

  Expected: compilation failure because MySQL repository constructors do not exist.

- [ ] **Step 3: Implement repositories and inject them in production**

  Use `ExecContext` and `QueryRowContext` exclusively. Insert UTC timestamps with `UTC_TIMESTAMP(6)`, obtain `LastInsertId`, and select the inserted row before returning it. Convert `sql.ErrNoRows`, `*mysql.MySQLError{Number: 1062}`, and `*mysql.MySQLError{Number: 1452}` to domain errors. In `order.Service.Create`, convert only `ErrUserNotFound` to the existing user-not-found client response; propagate other repository failures as internal errors.

  Refactor `newApplication` to receive repositories. Retain `newServer` only as the memory-backed test constructor; `main` must pass `user.NewMySQLRepository(db)` and `order.NewMySQLRepository(db)`.

- [ ] **Step 4: Verify Task 3**

  Run: `MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<temporary writable directory> go test ./internal/user ./internal/order ./cmd/api -run 'MySQL|TestUserAndOrderFlow|TestRejectsOrderForMissingUser'`

  Expected: repository constraints and existing HTTP business paths pass.

### Task 4: Cursor pagination across repositories, services, and HTTP

**Files:**
- Create: `internal/platform/page/page.go`
- Create: `internal/platform/page/page_test.go`
- Modify: `internal/platform/httpx/httpx.go`
- Modify: `internal/platform/httpx/httpx_test.go`
- Modify: `internal/user/model.go`
- Modify: `internal/order/model.go`
- Modify: `internal/user/repository.go`
- Modify: `internal/order/repository.go`
- Modify: `internal/user/service.go`
- Modify: `internal/order/service.go`
- Modify: `internal/user/handler.go`
- Modify: `internal/order/handler.go`
- Modify: `internal/user/mysql_repository.go`
- Modify: `internal/order/mysql_repository.go`
- Modify: `cmd/api/server_test.go`
- Modify: `README.md`

**Interfaces:**
- `page.Request{Limit int, AfterID int64}` and `page.Result[T]{Items []T, NextCursor string}`.
- `httpx.ParsePageRequest(r *http.Request) (page.Request, error)` defaults limit to `20` and accepts only `1 <= limit <= 100` and positive `afterId`.
- User and order repository `List` methods become `List(ctx context.Context, request page.Request) (page.Result[T], error)`.

- [ ] **Step 1: Write failing pagination tests**

  Add parser tests for default limit, `limit=3&afterId=12`, zero/over-100 limits, and nonnumeric/zero cursor values. Add memory repository tests with IDs 1–3 and request `{Limit: 2}` that expect item IDs `[1,2]` and `NextCursor="2"`; request `{Limit: 2, AfterID: 2}` must expect `[3]` and empty cursor. Add HTTP tests that assert `GET /users?limit=1` returns `{"items":[...],"nextCursor":"1"}` and that invalid pagination returns JSON 400.

- [ ] **Step 2: Verify the pagination tests fail**

  Run: `env GOCACHE=<temporary writable directory> go test ./internal/platform/page ./internal/platform/httpx ./internal/user ./internal/order ./cmd/api -run 'TestParsePageRequest|Test.*Pagination|TestUsersList'`

  Expected: compilation failure for the page types and parser, followed by HTTP response mismatch until handlers are changed.

- [ ] **Step 3: Implement page contract and both storage paths**

  Implement the generic `page.Result[T]` with `json:"items"` and `json:"nextCursor,omitempty"`. Parse query values in `httpx`; do not let domain packages import `net/http`. Change memory repositories to sort IDs, filter `ID > AfterID`, read `Limit + 1`, and set the cursor to the final returned ID only when an extra record exists. Implement equivalent MySQL queries using `WHERE id > ? ORDER BY id ASC LIMIT ?`.

  Change service and handler list signatures to use the page contract. Keep POST and `/:id` routes unchanged. Update README examples and the architecture document to show `limit` and `afterId`.

- [ ] **Step 4: Verify Task 4**

  Run: `MYSQL_TEST_DSN='<Compose DSN>' env GOCACHE=<temporary writable directory> go test ./...`

  Expected: memory and MySQL pagination tests, HTTP tests, and existing create/find paths pass.

### Task 5: End-to-end acceptance and documentation review

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/images/architecture-overview.svg`

- [ ] **Step 1: Run local service acceptance flow**

  Start MySQL with `docker compose up -d`, export the Compose DSN, and start `go run ./cmd/api`. Create two users, create an order for the first user, call `GET /users?limit=1`, then call `GET /users?limit=1&afterId=<nextCursor>`. Confirm restart preserves both users and the order.

- [ ] **Step 2: Update documentation**

  Document Compose startup, required `MYSQL_DSN`, migrations at startup, persistent-data behavior, and the paginated list response. Update the architecture diagram from memory maps to MySQL, connection pool, and embedded migration files.

- [ ] **Step 3: Run final verification**

  Run: `gofmt -d cmd/api/*.go internal/**/*.go`, `git diff --check`, `env GOCACHE=<temporary writable directory> go test ./...`, `env GOCACHE=<temporary writable directory> go vet ./...`, and `env GOCACHE=<temporary writable directory> go test -race ./...`.

  Expected: formatting produces no diff; all test, static, and race checks pass; integration tests run when `MYSQL_TEST_DSN` is supplied.
