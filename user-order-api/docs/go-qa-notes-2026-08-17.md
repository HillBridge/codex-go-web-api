# Go 学习问答笔记（2026-08-17）

本文档整理自 2026-08-17 的学习问答，围绕 `user-order-api` 项目的 `user` 模块代码逐行深挖，主题覆盖构造函数与依赖注入、方法接收者（receiver）机制、指针与取地址、包可见性、struct 与 interface 的分工、内置类型、struct tag，以及跨包同名函数等 Go 基础语法。

## 一、构造函数与依赖注入

### `NewHandler` 这类写法的含义

```go
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}
```

- Go 没有类构造函数语法，`NewXxx(依赖...) *Xxx` 是社区约定的构造函数写法
- `service` 作为参数传入，而不是在函数内部自己创建，这样 `Handler` 不依赖 `Service` 的具体创建逻辑，测试时可以传入 mock/fake
- 返回指针 `*Handler`：因为后续方法都用指针接收者定义，返回指针避免结构体拷贝，也让状态可以被共享
- `&Handler{service: service}` 用显式字段名初始化（key: value），比位置初始化更清晰、不易在加字段后错位

### 为什么要依赖注入，而不是直接在包内互相引用

同包内文件确实可以互相访问对方的类型/函数（见下文"包可见性"），但这只解决"语法上能不能引用"，不解决"架构上该怎么组织依赖"：

- **可测试性**：`Service` 只依赖 `Repository` 接口，测试时可以换成内存假实现，不用连真实数据库
- **解耦**：Handler 不知道 Repository 存在，Repository 不知道 HTTP 是什么，每层只认识"下一层的接口"
- **集中管理依赖关系**：所有组装逻辑收在 `cmd/api/server.go` 的 `newServer()` 里，一眼能看出整个程序用了哪些具体实现

代价：项目变大后手动组装代码会变长，规模足够大时才需要引入 Wire/fx 等 DI 工具自动生成组装代码。

### 依赖组装的完整链路（Handler → Service → Repository）

```go
userRepo    := user.NewMemoryRepository()             // Repository：最底层，负责数据存取
userService := user.NewService(userRepo, auditLogger)  // Service：接收 Repository，负责业务逻辑
userHandler := user.NewHandler(userService)             // Handler：接收 Service，负责 HTTP 层
```

- 依赖方向始终是"上层持有下层引用"，没有反向依赖
- `order.NewService(orderRepo, userService, auditLogger)` 里同时传入了 `orderRepo` 和 `userService`，说明下单逻辑需要调用用户模块的 Service（跨模块业务依赖），这种依赖是"服务间横向依赖"，不是 Handler 直接调用另一个 Handler
- 所有组装集中在 `newServer()` 一个函数里完成，是典型的**手动依赖注入（Manual DI）**

## 二、方法与接收者（receiver）

### `h.users` 为什么能调用到对应方法

```go
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/users", h.users)
	mux.HandleFunc("/users/", h.userByID)
}
```

- 方法声明 `func (h *Handler) users(...)` 把 `users` "挂"在 `*Handler` 类型上：任何 `*Handler` 类型的值都拥有这个方法
- `Register` 的接收者 `h` 也是 `*Handler` 类型，所以在方法体内写 `h.users`，编译器在 `*Handler` 类型上查找 `users` 方法，精确匹配、无歧义
- `h.users`（不加调用括号）是一个**方法值（method value）**，会生成一个闭包，把 `h` 固化在里面，类型正好是 `func(http.ResponseWriter, *http.Request)`，与 `http.HandleFunc` 需要的签名一致，因此可以直接传入

### `h *Handler` 里的 `h` 是什么

- `h` 是接收者的**自定义变量名**，纯粹是起的名字，作用域仅限该方法体内部，换成 `this`/`self` 完全不影响功能
- Go 社区惯例：用类型名首字母（小写）作接收者名，`Handler` → `h`，`Service` → `s`
- 接收者本质上是方法的"隐藏第一个参数"，跟普通形参（如 `w http.ResponseWriter`）是同一种机制，只是写在函数名前面的括号里

### 常见误解纠正：`h` 不是"给类型起别名后变成实例"

- `func (h *Handler) Register(...)` 这行代码本身**不创造任何实例**，只是声明"以后任何 `*Handler` 实例调用 `Register` 时，把该实例绑定到 `h`"
- 真正的实例是在 `NewHandler` 里 `&Handler{service: service}` 这一步创造出来的
- 调用 `userHandler.Register(mux)` 时，Go 才把 `userHandler` 这个已存在的实例地址传给 `h` —— 绑定发生在**每一次具体调用**，不是永久焊死在某份实例或某段代码上
- 类比：`func greet(name string)` 里 `name` 也只是占位符，`greet("Alice")` 调用时才被赋值成 `"Alice"`；`h *Handler` 与之同理，只是写在接收者位置而非普通参数列表

### 两种"绑定"要分开看：字段绑定 vs 接收者绑定

以 `h.service.List(ctx)` 为例：

1. **字段绑定（一次性，发生在构造时）**：`NewHandler(service)` 执行 `&Handler{service: service}` 时，把 `userService` 的地址**永久**写入 `service` 字段，此后不会变
2. **接收者绑定（每次调用都重新发生）**：
   - `h.users(w, r)` 被调用时，`h` 绑定为 `userHandler`
   - 方法体内 `h.service.List(ctx)` 又触发一次**独立的新绑定**：`List` 方法的接收者 `s` 被绑定为 `h.service` 取出来的那个 `Service` 实例
3. 因为字段值（第 1 步）不变，效果上看起来像"`h.service` 永远指向同一个 Service"，但两层绑定的性质不同：字段值是写入后固定的状态，接收者绑定是每次调用临时发生的行为

### 同一份实例 vs 同一份方法代码

- 方法代码本身**只有一份**，不会因为存在多个 `Handler` 实例就复制出多份函数体
- 不同实例调用同一个方法时，接收者绑定的对象不同：

```go
handlerA := NewHandler(serviceA)
handlerB := NewHandler(serviceB)

handlerA.Register(mux)   // 这次调用，h 绑定到 handlerA
handlerB.Register(mux)   // 这次调用，h 绑定到 handlerB
```

- 正确理解：**方法代码共享、固定；每一次调用才决定"当前操作的是哪份实例数据"**，不是"结构体应用到函数后永久绑定"

## 三、指针与取地址

### `&Handler{service: service}` 的含义

- `Handler{service: service}` 是**结构体字面量**，在内存中创建一份真实数据
- `&` 是**取地址符**，`&Handler{...}` 返回的是指向这份数据的**指针**，而不是数据本身的拷贝
- 返回类型 `*Handler` 中的 `*` 表示"指针类型"

为什么要返回指针而不是值：
1. **避免拷贝**：指针只传地址（固定大小），结构体变大时更高效
2. **保证共享状态**：`users`/`userByID`/`Register` 的接收者都是 `*Handler`，必须指向同一个实例，多次调用才能操作同一份数据

### 指针访问字段/方法的自动解引用语法糖

```go
h.service.List(ctx)
```

- `h` 是 `*Handler` 类型（一个地址），理论上要访问字段该写 `(*h).service`
- Go 对指针访问字段/方法做了语法糖：写 `h.service` 时编译器自动转换成 `(*h).service`，不需要手动解引用
- 反方向同理：值类型也能直接调用指针接收者的方法，Go 自动取地址
- 统一用 `.` 访问，不用像 C 那样区分 `.`（值）和 `->`（指针）

## 四、包可见性与同包访问

### import 一个包后为何能调用多个函数

```go
import "bridge-go/user-order-api/internal/user"
```

- import 导入的是**整个包**（该目录下所有 `package user` 的文件的集合），不是单个函数
- 一个包可以由多个文件组成（`handler.go`、`service.go`、`repository.go`），只要声明同一个 `package user`，编译时会被合并成一个逻辑单元
- 对外暴露靠**标识符首字母大小写**：大写（`NewHandler`、`NewService`）→ exported，包外可通过 `user.xxx` 访问；小写 → 包私有，import 了也调不到

### 同包文件间可以互相引用类型（`Handler` 里的 `Service` 从哪来）

```go
// handler.go
type Handler struct {
	service *Service   // Service 定义在 service.go，同包内不需要 import
}
```

- `handler.go` 和 `service.go` 都声明 `package user`，同一个包内的文件可以直接互相引用彼此的类型/函数/变量，不需要 import
- Go 的编译单元是**包**，不是**文件**：同包多个文件在编译器眼里被合并成整体

### 包外能调用什么，包内能调用什么（区分调用视角）

- `server.go`（`main` 包，包外视角）里的 `userHandler` 只能调用其**导出**的方法（如 `Register`），调不到 `h.users`（小写，包外不可见）
- `Handler` 自己的方法（`users`/`userByID`）内部，因为跟 `Handler`/`Service` 同属 `user` 包，才能访问 `h.service` 这种小写字段，并调用 `Service` 上的方法

## 五、struct 与 interface

### 区别与各自定位

| | struct | interface |
|---|---|---|
| 描述什么 | 数据长什么样（具体实现） | 必须能做什么（行为契约） |
| 是否有字段 | 有 | 没有，只有方法签名 |
| 是否占内存 | 是，真实数据 | 不直接占内存，是类型约束 |
| 是否必须使用 | 几乎必须（表达数据） | 按需引入（换取解耦/可测试性），非必须 |

```go
type Repository interface {
	Create(ctx context.Context, input CreateUserRequest) (User, error)
	List(ctx context.Context) ([]User, error)
	FindByID(ctx context.Context, id int64) (User, error)
}

type MemoryRepository struct {
	mu      sync.RWMutex
	nextID  int64
	users   map[int64]User
	byEmail map[string]int64
}
```

- `Service` 依赖 `Repository`（接口），只知道"有一个能 Create/List/FindByID 的东西"，不关心底层是内存还是数据库实现
- `MemoryRepository`（struct）是接口的一种具体实现
- Go 是**隐式接口实现**：只要 `MemoryRepository` 实现了接口要求的全部方法，Go 自动认为它满足该接口，不需要显式 `implements` 声明
- 传参时会自动把具体类型（`*MemoryRepository`）"装箱"成接口类型（`Repository`）

## 六、内置类型

- **`int`/`int64`**：整数类型，本项目用 `int64` 做用户 ID
- **`string`**：字符串，不可变，一旦创建不能修改内容
- **`slice`**（`[]T`）：动态数组，长度可变（`append` 追加），区别于固定长度数组 `[5]int`
- **`map[K]V`**：键值对集合，等价 JS 的 Object/Map、Python 的 dict

## 七、`MemoryRepository` 结构解读

```go
type MemoryRepository struct {
	mu      sync.RWMutex     // 并发锁
	nextID  int64            // 自增 ID 计数器
	users   map[int64]User   // 主表：ID -> 用户数据
	byEmail map[string]int64 // 索引表：Email -> ID
}
```

- `mu sync.RWMutex`：保护并发安全。写操作（`Create`）用 `Lock()`/`Unlock()` 独占锁；读操作（`List`/`FindByID`）用 `RLock()`/`RUnlock()` 共享锁，允许多个读并发但阻塞写
- `nextID`：模拟数据库自增主键，初始值 1，每创建一个用户 `+1`
- `users`：主存储，按 ID 查询 O(1)
- `byEmail`：辅助索引，检查邮箱唯一性时避免遍历整个 `users`（O(1) vs O(n)），是典型的"用空间换时间"索引思路

整体定位：用内置类型 + 锁模拟出一个线程安全、支持按 ID/Email 快速查找的临时数据库，替换成 MySQL 实现时 `Service` 层不用改一行代码。

## 八、struct tag 与 JSON 序列化

```go
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}
```

- 每个字段后面的 `` `json:"id"` `` 是**struct tag**（结构体标签）：附加在字段上的元数据字符串，不影响字段类型/值，可被反射机制读取
- 作用：告诉 `encoding/json` 包，序列化成 JSON 时用 `"id"` 作为 key（而非默认字段名 `"ID"`），反序列化时按此 key 匹配
- `httpx.WriteJSON`/`httpx.DecodeJSON` 底层就是靠读这些 tag 完成字段名映射

## 九、跨包同名函数不冲突

```go
// user 包
func NewService(repo Repository, audit audit.Logger) *Service { ... }   // 两个参数

// order 包
func NewService(repo Repository, users UserFinder, audit audit.Logger) *Service { ... }  // 三个参数
```

- 两者是完全不同的函数，只是恰好同名
- 函数名的完整"身份"包含它所属的包，调用时靠 `user.` / `order.` 前缀区分，不会冲突
- 调用时要认准是哪个包的 `NewService`，再对应它自己的参数列表，不要用另一个包的签名去套

## 十、后续学习路线（尚未覆盖，按优先级排序）

**语言核心机制**
- 错误处理：`error` 接口、`errors.New`/`errors.Is`/`errors.As`、`fmt.Errorf` + `%w` 包装
- 切片底层原理：长度 vs 容量、`append` 扩容机制、共享底层数组的坑
- 零值（zero value）概念：Go 没有"未初始化"状态
- `defer`/`panic`/`recover` 完整语义

**并发（Go 招牌特性，写后端服务绕不开）**
- goroutine、channel、`select` 多路复用（部分已见于 2026-08-14 笔记）
- `sync` 包：`Mutex`/`RWMutex`/`WaitGroup`
- `context.Context` 的传播机制、取消信号、`WithValue`

**工程实践**
- `go.mod`/模块系统与依赖管理
- 测试：`_test.go`、`testing` 包、表驱动测试
- 接口进阶：多接口实现、nil 接口 vs 接口包裹 nil 指针的陷阱
- 泛型（Go 1.18+ 的 `[T any]` 写法）

建议顺序：先补 **error 处理 + defer**（当前代码里到处都是），再看**并发**（`audit.NewAsyncLogger` 是现成的 goroutine 实例），最后补**测试写法和 go.mod**。
