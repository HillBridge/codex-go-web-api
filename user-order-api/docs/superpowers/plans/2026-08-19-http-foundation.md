# HTTP Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the API safe to operate as a single-process HTTP service by adding runtime configuration, server timeouts, graceful shutdown, protected HTTP boundaries, and strict request parsing.

**Architecture:** Keep `cmd/api` as the composition root. Add a small configuration and lifecycle layer there; keep HTTP parsing and response helpers in `internal/platform/httpx`; wrap the mux with recovery and access-log middleware. No external dependency is introduced.

**Tech Stack:** Go 1.25.3, Go standard library (`net/http`, `os/signal`, `context`, `log/slog`, `encoding/json`).

**Spec:** Phase 1 from the implementation sequence agreed in the Codex task.

## Global Constraints

- Preserve all existing routes and successful response bodies.
- Use the Go standard library only.
- Every behavior change starts with a focused failing test.
- Validate with `go test ./...`, `go vet ./...`, and `go test -race ./...` using an isolated Go build cache when needed.

---

### Task 1: Configurable and bounded HTTP server

**Files:**
- Create: `cmd/api/config.go`
- Create: `cmd/api/config_test.go`
- Modify: `cmd/api/main.go`

**Interfaces:**
- Produces `loadConfig(getenv func(string) string) (config, error)`.
- Produces `newHTTPServer(config, http.Handler) *http.Server`.
- Produces `serveUntilCancelled(context.Context, *http.Server, net.Listener, time.Duration) error`.

- [x] **Step 1: Write failing configuration and server-property tests**

  Assert that an empty environment produces `:8888`, that `PORT=9090` produces `:9090`, invalid ports fail, and the constructed server has non-zero read-header, read, write, and idle timeouts.

- [x] **Step 2: Run the focused tests and verify they fail because the new API is missing**

  Run: `go test ./cmd/api -run 'TestLoadConfig|TestNewHTTPServer'`

- [x] **Step 3: Implement the minimal config and server factory**

  Use `PORT` as an optional decimal port. Set `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` to explicit finite defaults. Make `main` load config and construct the server through the factory.

- [x] **Step 4: Add graceful shutdown behavior and its failing test**

  Start a server on `127.0.0.1:0`, cancel a context, and assert `serveUntilCancelled` returns without a serve error and within a bounded test deadline.

- [x] **Step 5: Implement graceful shutdown and verify Task 1**

  Listen explicitly, serve in a goroutine, and on context cancellation call `Shutdown` with the configured timeout. In `main`, create a signal context for interrupt and `SIGTERM`.

  Run: `go test ./cmd/api -run 'TestLoadConfig|TestNewHTTPServer|TestServeUntilCancelled'`

### Task 2: Recoverable and observable request boundary

**Files:**
- Modify: `cmd/api/server.go`
- Modify: `cmd/api/server_test.go`

**Interfaces:**
- Produces middleware that records request method, path, status and duration.
- Produces middleware that turns panics into a JSON 500 response.

- [x] **Step 1: Write failing middleware tests**

  Assert a normal request receives an `X-Request-ID` response header and that a handler panic results in status 500 with `application/json` content type and `{"error":"internal server error"}` body.

- [x] **Step 2: Run focused middleware tests and verify they fail**

  Run: `go test ./cmd/api -run 'TestRequestIDMiddleware|TestRecoveryMiddleware'`

- [x] **Step 3: Implement the minimal middleware chain**

  Generate a cryptographically random request ID when no `X-Request-ID` is supplied, preserve a caller-supplied ID, wrap `ResponseWriter` to capture final status, and log method, path, status, duration and request ID. Recover panics, log them, and use `httpx.WriteError` for the 500 response.

- [x] **Step 4: Verify Task 2**

  Run: `go test ./cmd/api -run 'TestRequestIDMiddleware|TestRecoveryMiddleware|TestUserAndOrderFlow'`

### Task 3: Strict JSON and consistent method responses

**Files:**
- Create: `internal/platform/httpx/httpx_test.go`
- Modify: `internal/platform/httpx/httpx.go`
- Modify: `cmd/api/server.go`
- Modify: `internal/user/handler.go`
- Modify: `internal/order/handler.go`
- Modify: `cmd/api/server_test.go`

**Interfaces:**
- `DecodeJSON(http.ResponseWriter, *http.Request, any) error` rejects non-JSON content types, a body over 1 MiB, malformed JSON, and trailing JSON values.
- `WriteMethodNotAllowed(http.ResponseWriter, allow string)` gives all routes a JSON 405 response and an `Allow` header.

- [x] **Step 1: Write failing parsing and method tests**

  Cover missing/wrong content type, `{"name":"Ada"}{}`, a body larger than 1 MiB, and a `PUT /users` response with status 405, `Allow: GET, POST`, and JSON error content type.

- [x] **Step 2: Run focused tests and verify they fail**

  Run: `go test ./internal/platform/httpx ./cmd/api -run 'TestDecodeJSON|TestUsersMethodNotAllowed'`

- [x] **Step 3: Implement minimal strict decoding and 405 helper**

  Parse media types with `mime.ParseMediaType`, cap request bodies with `http.MaxBytesReader`, decode exactly one JSON value, and return stable `AppError` messages. Replace direct `WriteHeader(http.StatusMethodNotAllowed)` calls with the shared helper.

- [x] **Step 4: Verify Task 3**

  Run: `go test ./internal/platform/httpx ./cmd/api -run 'TestDecodeJSON|TestUsersMethodNotAllowed|TestUserAndOrderFlow'`

### Task 4: Phase acceptance verification

**Files:**
- Modify: `README.md` only if runtime configuration needs documentation.

- [x] **Step 1: Run formatting and focused regression checks**

  Run: `gofmt -w cmd/api/*.go internal/platform/httpx/*.go internal/user/handler.go internal/order/handler.go && go test ./...`

- [x] **Step 2: Run static and concurrency verification with an isolated cache**

  Run: `go vet ./...` and `env GOCACHE=<temporary writable directory> go test -race ./...`.

- [x] **Step 3: Review the diff against the phase scope**

  Confirm: no third-party dependency; existing endpoint success paths remain; server has finite timeouts; shutdown is signal-driven; malformed and oversized JSON are rejected; 405 responses are consistent; panic becomes JSON 500.
