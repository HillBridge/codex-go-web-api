# Go 学习问答笔记（2026-08-18）

本文档整理自 2026-08-18 的学习问答，覆盖 struct tag、错误处理体系（`errors.New`/`errors.Is`/`errors.As`/`fmt.Errorf`+`%w`/错误链/`Unwrap`）、JSON 序列化边界、slice 底层原理、零值、`defer`/`panic`/`recover`、channel、`context.Context`、鸭子类型、`AsyncLogger.Record` 的异步机制逐行解读，以及 HTTP 中间件的组装顺序与执行时机。全部结合 `user-order-api` 项目实际代码举例。

## 一、struct tag

```go
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}
```

- tag 是写在字段后面的字符串元数据，编译器不解释它，运行时靠 `reflect.StructTag` 读取
- 应用场景本质：给某个库一个约定好的方式，去控制"这个字段该怎么被处理"——`encoding/json` 用它决定序列化/反序列化时的 JSON key
- 一个字段可以同时挂多个库的 tag（`json:"id" db:"id" validate:"required"`），互不干扰，因为每个库只认自己的 key
- 常见变体：`json:"name,omitempty"`（零值省略字段）、`json:"-"`（永不参与 JSON）

## 二、错误处理体系

### `errors.New` / `fmt.Errorf`+`%w` / `errors.Is` / `errors.As` 分工

| | 在问什么 |
|---|---|
| `errors.New(...)` | 造一个独一无二的"哨兵错误"，以后靠它认身份 |
| `fmt.Errorf("...: %w", err)` | 造一个新错误，把 `err` 包进它肚子里（套娃） |
| `errors.Is(err, target)` | err 这条链里，有没有跟 `target` **同一个值**的？ |
| `errors.As(err, &out)` | err 这条链里，有没有 `out` **这个类型**的？有就取出来 |

项目例子（[repository.go](../internal/user/repository.go)、[service.go](../internal/user/service.go)）：

```go
var ErrEmailTaken = errors.New("email already exists")   // 哨兵错误

// repo 层直接返回哨兵值
return User{}, ErrEmailTaken

// service 层按值判断
if errors.Is(err, ErrEmailTaken) {
	return User{}, httpx.BadRequest("email already exists")   // 换成全新的 *AppError，链在这断开
}

// 未知错误：包装，保留原始错误
return User{}, httpx.Internal("failed to create user", fmt.Errorf("create user: %w", err))
```

判断标准：错误只是个"空壳标记"（除了"是不是它"没别的信息）→ 用 `errors.Is`；错误是个 struct，携带了要读的字段（比如 `*AppError` 要读 `Status`）→ 用 `errors.As`。两者可以在同一条错误链上共存。

### 错误链是怎么形成的？不是 `errors` 包维护的

`errors` 包本身不维护任何链表结构，链是**每个错误类型自己实现 `Unwrap() error` 方法**形成的"隐式链表"，`errors.Is`/`errors.As` 只是知道去调这个方法而已：

```go
// httpx.go
func (e *AppError) Unwrap() error {
	return e.Err
}
```

`errors.Is`/`errors.As` 内部本质是个 for 循环：判断当前 err 不中，就调它的 `Unwrap()` 拿到下一层，继续判断，直到 `Unwrap()` 返回 `nil`（或者这个类型压根没有 `Unwrap()`）为止：

```go
func Is(err, target error) bool {
	for {
		if err == target { return true }
		u, ok := err.(interface{ Unwrap() error })
		if !ok { return false }
		err = u.Unwrap()
		if err == nil { return false }
	}
}
```

所以"能不能被穿透查找"完全取决于这个类型**有没有实现 `Unwrap()` 这个方法签名**——不需要注册、不需要继承基类，跟前面"鸭子类型"是同一套规则（见下文第九节）。[httpx.go](../internal/platform/httpx/httpx.go) 里 `*AppError` 因为写了这个方法，才能在 `fmt.Errorf("create user: %w", err)` → `httpx.Internal(...)` 这种套了好几层的场景下，仍然被最外层的 `errors.As(err, &appErr)` 顺着链找到。

### `Error() string` 和 `Unwrap() error` 是两个完全不同用途的方法

```go
func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()   // 有底层错误，递归下去，拿最原始那句话
	}
	return e.Message           // 没有底层错误，返回给用户看的文案
}

func (e *AppError) Unwrap() error {
	return e.Err                // 只吐出一层，不做任何加工，给 errors.Is/As 的循环用
}
```

| | `Error() string` | `Unwrap() error` |
|---|---|---|
| 返回什么 | 字符串（给人/日志看） | 一个 error 值（给程序继续处理） |
| 会不会自己递归 | 会 | 不会，只吐一层 |
| 谁来调 | 日志、`fmt`、任何把它当 `error` 用的代码 | `errors.Is`/`errors.As` 内部循环 |

项目现状：[httpx.go](../internal/platform/httpx/httpx.go) 里 `WriteError` 写 HTTP 响应时直接读 `appErr.Message` 字段，**没有调 `.Error()`**——客户端永远只看到 `Message`。`Error()` 方法目前项目里**没有任何地方真的被调用**（[server.go](../cmd/api/server.go) 的 `requestLogMiddleware` 只打 method/path，没打 err），是为将来加日志预留的通道：一旦哪天写 `logger.Error("...", "err", err)` 或 `fmt.Println(err)`，Go 会自动调 `.Error()`，这时候能拿到最底层的真实原因，而不是"failed to create user"这种空话反复出现。

### `var appErr *AppError` 和 `errors.As(err, &appErr)`

```go
var appErr *AppError        // 声明一个 *AppError 类型的变量，零值是 nil
if errors.As(err, &appErr) {
	WriteJSON(w, appErr.Status, ...)
}
```

- `errors.As` 需要**往目标变量里写回结果**，所以传的必须是指针（这里是 `&appErr`，类型 `**AppError`）
- 匹配成功后，`errors.As` 通过这个地址把 `appErr` 改写成链上找到的那个具体实例，之后才能读 `appErr.Status`
- 没匹配到，`appErr` 保持 `nil`，`if` 分支不会进去，不存在对 nil 指针取字段导致 panic 的风险

### `error` 接口本身

```go
type error interface {
	Error() string
}
```

- 只要某个类型有一个 `Error() string` 方法，就自动满足这个接口，不需要任何显式声明——`error` 是 Go 的预声明标识符，跟 `int`/`string` 一样内置，但本质就是个普通接口
- `error` 类型的变量内部存的是 `(动态类型, 动态值)` 组合，不是字符串本身——`var err error = httpx.BadRequest("x")` 里 `err` 记的是 `(动态类型=*AppError, 动态值=指针)`，这就是 `errors.As` 能"还原"出具体类型的原理

### `return User{}, httpx.BadRequest("email already exists")` 什么意思

- `httpx.BadRequest("email already exists")` 构造并返回一个 `&AppError{Status:400, Message:"email already exists", Err:nil}`——`Err` 是 `nil`，因为这是业务主动判定的错误，没有更底层的原因
- `User{}` 是零值 struct 占位，因为函数签名要求两个返回值（`(User, error)`），这一分支没有创建出真正的用户，第一个返回值没意义，调用方约定是"先看 error 非 nil，就不要用第一个返回值"

## 三、JSON 序列化边界

反序列化（[handler.go](../internal/user/handler.go) `httpx.DecodeJSON`）用了 `DisallowUnknownFields()`：

- **只挡多传，不挡少传**：JSON 里出现 struct 没有的字段（如多传一个 `age`）会报错；但 JSON 缺少某个字段（如没传 `email`），解码不会报错，那个字段直接是零值（`""`），不会主动校验"必填"
- 必填/格式校验是 service 层手写代码另外做的（[service.go](../internal/user/service.go)）：
  ```go
  if strings.TrimSpace(input.Name) == "" {
      return User{}, httpx.BadRequest("name is required")
  }
  ```
- 序列化时（`json.NewEncoder(w).Encode(user)`）按 tag 把字段名从 Go 的大写导出名换成小写 key，`time.Time` 被编码成 RFC3339 字符串（这是 `time.Time` 自己实现 `MarshalJSON` 的结果，不是 tag 决定的）

## 四、slice 底层原理

slice 运行时是三元组 `{ptr, len, cap}`，本身不持有数据，指向一个底层数组。

```go
users := make([]User, 0, len(r.users))   // cap 预先给够，避免 append 时反复扩容
for _, item := range r.users {
	users = append(users, item)
}
```

- `len == cap` 时再 `append`，触发重新分配一块更大的底层数组，拷贝旧数据过去
- **共享底层数组的坑**：多个 slice 可能指向同一块底层数组，只要 cap 还有余量，`append` 就是"就地修改"，会波及所有共享这块数组的 slice：
  ```go
  a := []int{1, 2, 3, 4, 5}
  b := a[1:3]        // b 和 a 共享底层数组
  b = append(b, 99)  // cap(b) 还有余量 → 直接改了 a 的底层数组，a[3] 从 4 变成 99
  ```
- 只有触发重新分配后，返回的新 slice 才和原来彻底分家

## 五、零值（zero value）

- `int`→`0`，`string`→`""`，`bool`→`false`，指针/slice/map/chan/func/interface→`nil`，struct→每个字段递归取零值
- Go 没有 `null`，函数不能"什么都不返回"，只能返回明确的零值 + error：
  ```go
  user, exists := r.users[id]
  if !exists {
      return User{}, ErrNotFound   // User{} 就是零值，配合 error 表示"别用它"
  }
  ```
- `nil` slice/map 可以读（返回零值），但**写 nil map 会 panic**，所以构造函数里必须显式 `make(map[...])`，不能依赖零值

## 六、defer / panic / recover

- 多个 `defer` 按 **LIFO**（后进先出）顺序执行
- 最典型用法：锁释放和 context 取消，保证不管哪个分支 `return`，收尾动作都不会漏：
  ```go
  r.mu.Lock()
  defer r.mu.Unlock()
  ```
- `panic` 会沿调用栈往上展开，沿途 `defer` 仍会执行；某个 `defer` 里调 `recover()` 能拦住 panic，让程序恢复正常执行
- 这个项目没有手写 `panic`/`recover`——`net/http` 本身在每个请求的 goroutine 外层已经带了 recover（handler 里 panic 不会拖垮整个进程，只中断这一个请求），所以没必要再包一层中间件

### `defer cancel()` 具体在防什么

```go
auditCtx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
```

- `WithTimeout` 内部起了一个定时器，到期会自动取消；但如果任务提前跑完却不调 `cancel()`，这个定时器会一直占着资源，直到真正到期才被清理
- `defer cancel()` 保证不管 goroutine 从哪条路径退出，都立刻释放这个定时器资源，不用白等到超时——跟 `defer r.mu.Unlock()` 是同一个动机
- `cancel()` 是幂等的，多次调用（比如超时自动触发一次，`defer` 又调一次）完全安全

## 七、channel

```go
ch := make(chan int)        // 无缓冲：发送/接收必须同时就位
ch := make(chan int, 10)    // 有缓冲：没满就能发送不阻塞
```

项目里没有裸写 `chan`，但 `context.Context` 的 `ctx.Done()` 本身就返回一个 `<-chan struct{}`，[audit.go](../internal/platform/audit/audit.go) 的 `select` 就是活的 channel 用法：

```go
select {
case <-ctx.Done():
	...
	return
case <-auditCtx.Done():
	return
default:
	...   // 真正写日志
}
```

**关键细节**：这里因为有 `default`，`select` 不会阻塞等待，而是在执行到这一行的**那一瞬间**做一次非阻塞快照检查——问"此刻 `ctx` 是不是已经被取消了？此刻 `auditCtx`（刚创建，给了 1 秒）是不是已经到期了？"两个都没有，才走 `default`。它不是"持续监听、谁先发生走谁"，`default` 的存在让语义变成了"一次性轮询"。

## 八、context.Context

最大作用：给一条跨函数、跨 goroutine 的调用链，提供一个统一传递的"生命周期句柄"，让状态能沿调用链自动向下扩散。三件事：

1. **取消信号传播**：父 context 取消，所有派生的子 context 自动跟着取消
2. **超时/截止时间**：`WithTimeout`/`WithDeadline` 把"最多能做多久"变成取消信号的一种来源
3. **（项目未用）`WithValue`**：传 trace ID、认证身份等"贯穿全链路但不该当显式参数"的东西；key 必须用自定义不导出类型，不能滥用来传必需参数

调用链传播路径：

```
r.Context()（http.Request 自带，客户端断开自动取消）
  └─ handler.go: context.WithTimeout(r.Context(), 2*time.Second) → ctx
       └─ service.Create(ctx, ...)
            └─ repo.Create(ctx, ...)  → ctx.Err() 检查
```

心智模型：context 是**一棵树**，不是一条链，取消只能从根往叶子单向扩散，叶子取消不影响父节点。这正是 [audit.go](../internal/platform/audit/audit.go) 里 `auditCtx` 故意用 `context.Background()` 另起一个根、不挂在请求 `ctx` 下面的原因——否则请求一结束，审计的 context 会被连带取消，日志永远没机会写。

### `requestContext` 辅助函数从哪来

```go
// handler.go，包内私有函数
func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Second)
}
```

不是标准库的东西，是项目自己封装的辅助函数，把"从请求 context 派生 2 秒超时子 context"这句常用逻辑抽出来，避免在 `users`/`userByID` 两个 handler 方法里重复写。首字母小写，包私有，包外调不到。

## 九、鸭子类型（duck typing）与接口设计

"如果它走起来像鸭子、叫起来像鸭子，那它就是鸭子"——Go 判断一个类型是否满足接口，看它有没有对应的方法（行为），不看它有没有显式声明"我实现了这个接口"。对比 Java/C# 的名义类型（必须写 `implements`），Go 完全靠方法签名结构匹配，编译器在**使用**的地方（赋值/传参）才检查。

```go
// audit.go
type Logger interface {
	Record(ctx context.Context, action string, fields map[string]any)
}

type AsyncLogger struct{ logger *slog.Logger }
func (l *AsyncLogger) Record(ctx context.Context, action string, fields map[string]any) { ... }
// AsyncLogger 的定义里完全没提 Logger 接口，两者的关系是在 main.go 组装时才成立的
```

`Logger` 接口方法没有返回值——意味着"记审计日志失败"被设计成不该影响主流程，调用方也没办法判断它有没有成功，呼应 `AsyncLogger.Record` 内部整个异步执行（见下节）。

`Service` 依赖 `audit.Logger`（接口）而不是 `*audit.AsyncLogger`（具体类型），是依赖倒置：测试时可以随手做一个满足这个方法签名的假类型传进去，不需要真的起异步 goroutine、不需要跟 `audit` 包有任何关系。`Repository` 接口也是同样的设计动机。

`order` 包里还有一个更明显的例子（[order/service.go](../internal/order/service.go)）：

```go
type UserFinder interface {
	FindByID(ctx context.Context, id int64) (user.User, error)
}
```

`order` 包自己定义了一个只要求 `FindByID` 一个方法的接口，`user.Service`（有更多方法）自动满足它——这是"接口在消费者一侧定义，只声明自己需要的最小行为"的典型 Go 惯用法。

## 十、`AsyncLogger.Record` 逐行解读：为什么异步

```go
func (l *AsyncLogger) Record(ctx context.Context, action string, fields map[string]any) {
	go func() {
		auditCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		select {
		case <-ctx.Done():
			l.logger.InfoContext(auditCtx, "audit skipped because request ended", "action", action)
			return
		case <-auditCtx.Done():
			return
		default:
			args := []any{"action", action}
			for key, value := range fields {
				args = append(args, key, value)
			}
			l.logger.InfoContext(auditCtx, "audit record", args...)
		}
	}()
}
```

- `go func(){...}()` 这一行执行完，`Record` 方法本体就结束了——调用方（`service.Create`）几乎立刻能往下走，不等大括号里的内容跑完，两者并发执行，谁先谁后没有保证
- 好处：审计日志的 I/O 开销不会拖慢用户实际请求的响应时间；代价：响应可能在审计日志真正写出去之前就已经返回给客户端了，这是"fire-and-forget"式日志的典型权衡，不适合需要强一致保证的场景（比如扣款记录）
- `args := []any{"action", action}` + `for range fields { append(args, key, value) }`：把 `map[string]any` 拍扁成交替的 `[key, value, key, value, ...]` 扁平切片，因为 `slog.InfoContext` 的可变参数要求这种格式
- 日志用的 context 是 `auditCtx`（自己的 1 秒生命周期），不是外面传入的 `ctx`——因为这条日志的生命周期跟着审计任务走，不跟着可能已经结束的请求走

## 十一、HTTP 中间件的组装顺序与执行时机

```go
// server.go
func newServer() http.Handler {
	...
	mux := http.NewServeMux()
	... 注册各种路由到 mux ...
	return requestLogMiddleware(logger, mux)   // 最外层包装写在最后
}

func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
```

**为什么写在最后**：中间件必须是"由内向外"组装——先把最核心的处理逻辑（`mux`，已注册好所有路由）准备好，再拿它当参数套上外层装饰逻辑。写在最后一行，是因为它逻辑上必须是最外层，要在"进入具体路由处理之前"先记一条日志，不管请求最终落到哪个 handler。

**执行时机分两个阶段**：
1. **构建阶段（只跑一次）**：程序启动时 `main()` 调用一次 `newServer()`，从头跑到尾拼出最终的 `http.Handler`，存进 `server.Handler`，之后一直复用这**同一个**值
2. **请求处理阶段（每个请求都跑一次）**：`net/http` 收到每个请求都调用 `server.Handler.ServeHTTP(w, r)`，也就是 `requestLogMiddleware` 返回的闭包——**先打日志，再调 `next.ServeHTTP(w, r)`（即 `mux`）分发到具体 handler**：

```
请求进来 → requestLogMiddleware 闭包：打印一行日志 → mux.ServeHTTP：查路由 → 具体 handler 处理业务
```

## 十二、后续学习路线更新

本次已覆盖第 2026-08-17 笔记里"尚未覆盖"清单中的：错误处理完整体系、零值、`defer`/`panic`/`recover`、`context.Context` 传播机制与取消信号、slice 底层原理，以及部分并发（channel、goroutine 异步）。

**仍未覆盖**：
- `sync` 包：`Mutex`/`RWMutex`（`MemoryRepository` 已用到，但没深挖读写锁的区别和用法边界）/`WaitGroup`
- `context.WithValue` 的真实代码示例（项目暂无落地场景）
- `go.mod`/模块系统与依赖管理
- 测试：`_test.go`、`testing` 包、表驱动测试
- 接口进阶：多接口实现、nil 接口 vs 接口包裹 nil 指针的陷阱
- 泛型（`[T any]` 写法）
- 项目整体架构图（本次请求中途被打断，尚未产出）
