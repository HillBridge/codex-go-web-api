# Go 学习问答笔记（2026-08-19）

本文档整理自 2026-08-19 的学习问答，围绕 MySQL 持久化改造展开：连接池的创建与校验（`database.Open`）、连接池参数（`MaxOpenConns`/`MaxIdleConns`/`ConnMaxLifetime`/`PingContext` 超时）的含义与取值依据、嵌入式向前迁移机制（`database.ApplyMigrations`）、order 模块从 HTTP 到 SQL 的完整分层链路，以及 DSN 连接字符串和"什么算迁移"的概念澄清。全部结合 `user-order-api` 项目实际代码举例。

## 一、`database.Open`：连接池的创建与校验

[internal/platform/database/mysql.go](../internal/platform/database/mysql.go)：

```go
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	...
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(connectionLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping MySQL: %w", err)
	}
	return db, nil
}
```

- `_ "github.com/go-sql-driver/mysql"`：匿名导入，只为触发该包 `init()` 把自己注册进 `database/sql` 的驱动列表，本文件不直接调用它的任何符号。
- `sql.Open("mysql", dsn)` **不会**真正建立网络连接，只校验 DSN 格式、创建一个延迟初始化的连接池对象；真正的 TCP 连接在首次使用或下面的 `Ping` 时才建立。
- `PingContext` 主动发起一次连接尝试，验证 DSN 是否可达、账号密码是否正确——"fail fast"，在 `Open` 阶段就发现问题，而不是等到第一次业务查询才暴露。
- Ping 失败时先 `db.Close()` 释放已分配的连接池资源，再包装错误返回；`_ =` 表示故意忽略 Close 的错误（已经在错误返回路径上，Close 失败不影响主流程）。
- `defer cancel()` 保证 Ping 的超时子 context 无论成功失败都及时释放定时器资源。

## 二、`MaxOpenConns` vs `MaxIdleConns`

两者是包含关系：**空闲连接是打开连接的子集**。

- **`MaxOpenConns`（10）**：连接池**总共**能同时打开的连接数上限，包含正在使用（in-use）和闲置未用（idle）的。达到上限后，新查询会阻塞等待，直到有连接释放或 `ctx` 超时。防止连接数暴涨打垮 MySQL 服务端。
- **`MaxIdleConns`（5）**：连接使用完毕归还池子后，**最多保留多少个空闲连接**供下次复用。超过这个数量，多出来的连接直接关闭，不再保留。

类比：`MaxOpenConns=10` 是连接池最多能同时"借出+持有"10 把钥匙；`MaxIdleConns=5` 是钥匙还回来后，前台最多留 5 把备用，多出来的直接销毁。约定上 `MaxIdleConns <= MaxOpenConns`，超过会被 Go 自动钳制。

## 三、`connectionLifetime` vs `pingTimeout`

管的是完全不同的维度：一个管"连接能活多久"，一个管"启动检测最多等多久"。

| | `connectionLifetime`（30 分钟） | `pingTimeout`（5 秒） |
|---|---|---|
| 生效阶段 | 连接池整个运行期间，持续生效 | 仅 `Open` 时那一次 `PingContext` |
| 管的是 | 单个连接从创建起最长存活多久，超时后透明淘汰重建 | 启动探活最多等多久才判定失败 |
| 目的 | 防止连接老化/僵死（MySQL 端 `wait_timeout` 默认 8 小时会踢空闲连接；云数据库可能做故障转移） | 让启动失败快速暴露，而不是无限期挂着等 |

`connectionLifetime` 设得比 MySQL 默认 `wait_timeout` 短很多，就是为了让连接主动"新陈代谢"，抢先规避被服务端单方面踢掉、客户端还以为存活的"僵尸连接"问题（常见报错：`invalid connection`、`broken pipe`）。

## 四、这几个参数数值是哪来的

查了 [docs/superpowers/plans/2026-08-19-mysql-persistence-zh.md](superpowers/plans/2026-08-19-mysql-persistence-zh.md)，计划文档里只是预先写定了"最大连接数 10、最大空闲连接 5、连接最大生命周期 30 分钟、5 秒 PingContext"，**没有解释依据，也没有引用官方规范**——不是语言/框架层面的"标准"，而是社区常见的经验默认值组合：

- `MaxOpenConns=10`：合理值取决于 MySQL 的 `max_connections`、服务副本数、实际并发量；10 是小服务/教学项目级别的保守起手值，生产环境要结合压测和数据库容量重新调。
- `MaxIdleConns=5`：常见经验比例是 `MaxOpenConns` 的 30%~50%，在"复用省重建开销"和"闲置占资源"之间取平衡。
- `connectionLifetime=30min`：常见区间 5 分钟到 1 小时，只要明显短于 MySQL `wait_timeout` 和负载均衡器超时即可。
- `pingTimeout=5s`：启动健康检查类超时的典型默认（常见示例给 3~10 秒）。

结论：这是作者按经验拍定的默认基线，不是从项目其他配置或官方标准推导出来的，后续可以考虑做成可配置项。

## 五、`database.ApplyMigrations`：嵌入式向前迁移机制

[internal/platform/database/migrate.go](../internal/platform/database/migrate.go)：

```go
//go:embed migrations/*.sql
var migrations embed.FS
```

- `//go:embed` 是编译器指令（必须紧贴变量声明，中间不能有空行），编译时把 `migrations/*.sql` 打包进最终二进制，部署不需要额外携带 SQL 文件。
- `embed.FS` 是只读虚拟文件系统，运行时可 `ReadDir`/`ReadFile`。

`ApplyMigrations` 执行流程：

1. `CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL)`——记账表，`IF NOT EXISTS` 保证可重复安全执行。
2. 查出 `schema_migrations` 里已记录的文件名，存进 `map[string]bool` 方便 O(1) 判断。
3. `migrations.ReadDir("migrations")` 读出所有嵌入文件，再显式 `sort.Slice` 按文件名排序——数字前缀命名（`0001_users.sql`、`0002_orders.sql`）靠字典序保证执行顺序。
4. 逐个文件：跳过已执行的 → 执行 DDL → **成功后才**插入 `schema_migrations` 记录。失败时不写记录，下次重启会重新尝试同一个文件，保证失败可重试。
   - 取舍点：没有用事务包裹"执行 DDL + 插入记录"两步，理论上 DDL 成功但插入记录时崩溃会导致重复执行；靠约定"每个迁移文件只包含一条 DDL 语句"、写成幂等语句来兜底。

`appliedMigrations` 里两个容易漏的细节：
- `defer rows.Close()`：`*sql.Rows` 必须显式关闭，否则底层连接不会释放回连接池，是 `database/sql` 最经典的资源泄漏点。
- 遍历结束后必须检查 `rows.Err()`：`for rows.Next()` 退出可能是"正常读完"也可能是"读取中出错"，`Next()` 出错时只返回 `false`，错误要靠 `rows.Err()` 才能拿到。

`mysql.go` 负责"怎么连上库"，`migrate.go` 负责"连上库之后怎么把表结构建好"，`main.go` 里顺序调用：先 `Open`，再 `ApplyMigrations`。

## 六、order 模块如何接入数据库（完整分层链路）

**1. 启动阶段**（[cmd/api/main.go](../cmd/api/main.go)）：

```go
db, err := database.Open(startupCtx, config.MySQLDSN)
database.ApplyMigrations(startupCtx, db)
application := newApplication(logger, user.NewMySQLRepository(db), order.NewMySQLRepository(db))
```

`user` 和 `order` 两个模块共享同一个 `*sql.DB` 连接池，不是各开一份。

**2. `Repository` 接口**（[internal/order/repository.go](../internal/order/repository.go)）：只声明 `Create`/`List`/`FindByID` 三个方法，`Service` 只依赖接口，不关心背后是 `MemoryRepository`（`map` + `sync.RWMutex`，测试用）还是 `MySQLRepository`（生产用）——标准的依赖倒置写法。

**3. `MySQLRepository`**（[internal/order/mysql_repository.go](../internal/order/mysql_repository.go)）：

- `Create`：`INSERT ... VALUES (?, ?, ?, UTC_TIMESTAMP(6))` 参数化查询防注入；插入后用 `result.LastInsertId()` 拿到自增 ID，再调 `FindByID` 回查一次，保证返回值（比如 `created_at` 精确值）跟数据库里真实存的一致。外键错误码 `1452`（引用了不存在的 `users.id`）通过 `errors.As` 提取 `*mysql.MySQLError` 判断，翻译成业务错误 `ErrUserNotFound`，仓储层吸收数据库细节，只向上暴露领域错误。
- `List`：`WHERE id > ? ORDER BY id ASC LIMIT ?` 做游标分页（不用 `OFFSET`，避免大偏移量性能问题），`LIMIT Limit+1` 多查一条用来判断"是否还有下一页"。
- `FindByID`：单行查询用 `QueryRowContext`，把 `sql.ErrNoRows` 翻译成包内的 `ErrNotFound`。
- `scanOrder`：定义最小接口 `orderScanner{ Scan(...any) error }`，让 `*sql.Row`（单行）和 `*sql.Rows`（多行）都能传进来复用同一份字段映射逻辑，避免三处重复写。

**4. `Service`**（[internal/order/service.go](../internal/order/service.go)）：完全不知道底层是 MySQL，校验参数、查用户是否存在、调用 `s.repo.Create`，把仓储层的领域错误翻译成 HTTP 错误（`ErrUserNotFound`→400、`ErrNotFound`→404），成功后记一条审计日志。

一句话总结分层：`main.go` 建池子并注入 → `MySQLRepository` 封装 SQL 细节和 MySQL 特有错误码，只对外暴露 `Repository` 接口和领域错误 → `Service` 只依赖接口做业务编排 → `handler.go`（HTTP 层）再转成 HTTP 响应。`user` 模块是同构镜像结构。

## 七、`MySQLDSN` 是什么

DSN = Data Source Name，把"怎么连上这个数据库"编码成一个字符串。项目里的值（[.env.example](../.env.example)）：

```
MYSQL_DSN=app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true&charset=utf8mb4&loc=UTC
```

格式：`[用户名]:[密码]@[协议](地址:端口)/[数据库名]?[参数...]`

| 片段 | 值 | 含义 |
|---|---|---|
| 用户名:密码 | `app:app_password` | Compose 服务创建的应用账号 |
| 协议/地址/端口 | `tcp(127.0.0.1:3307)` | 本地 Docker Compose 映射端口（宿主机 3307 → 容器 3306） |
| 数据库名 | `user_order_api` | 连上后默认使用的 schema |
| `parseTime=true` | | 让驱动把 `DATETIME`/`TIMESTAMP` 解析成 Go 的 `time.Time`，而不是 `[]byte`——`scanOrder` 能直接 `Scan(&item.CreatedAt)` 到 `time.Time` 字段就靠它 |
| `charset=utf8mb4` | | 完整 Unicode 支持（含 emoji），避免 MySQL 默认 `utf8` 只支持 3 字节的坑 |
| `loc=UTC` | | 时间值按 UTC 解析/格式化，跟仓储层写入用的 `UTC_TIMESTAMP(6)` 保持一致 |

使用链路：[cmd/api/config.go](../cmd/api/config.go) 从环境变量 `MYSQL_DSN` 读取（必填，缺失直接报错退出）→ `main.go` 传给 `database.Open` 建连接池。`.env.example` 是本地开发的模板，真实的 `.env` 已加入 `.gitignore`，不会提交密码到仓库。

## 八、什么样的操作算"跑迁移"，什么时候需要跑

**算迁移的特征**：改的是**表结构本身**（DDL），且需要被"记账"（执行过不能/不该重复执行）——`CREATE TABLE`、`ALTER TABLE ADD/DROP COLUMN`、加索引/外键约束。项目里 `0001_users.sql`、`0002_orders.sql` 各建一张表就是典型例子。

**不算迁移**：`INSERT`/`UPDATE`/查询等业务数据操作（比如 `mysql_repository.go` 的 `Create` 方法），属于运行时正常业务流程，不进 `schema_migrations` 记账。区分标准：**这个变更需要 `schema_migrations` 记录"是否已执行"吗？**

**什么时候需要跑**：项目设计成**每次服务启动都自动跑**（`main.go:37` 调 `database.ApplyMigrations`），内部自己判断要不要真的执行——查 `schema_migrations` 已记录的文件名，跳过已跑过的，只执行新增的。所以：

1. **日常启动/部署**：不需要手动做任何事，新文件会在下次重启时被自动检测并执行。
2. **要新增/改表结构时**：手动新建一个新的 `.sql` 文件（数字前缀递增，如 `0003_xxx.sql`），写好 DDL，正常重启服务即可触发执行。**不要修改已提交的旧迁移文件**——它们可能已经在别的环境执行并记录过，改旧文件不会被重新执行，只会导致环境间表结构不一致。

## 九、后续学习路线更新

本次覆盖了 MySQL 持久化改造相关的全部新增代码：连接池配置与校验、嵌入式迁移机制、order 模块的完整数据库接入链路、DSN 格式、迁移概念澄清。

沿用 [2026-08-18 笔记](go-qa-notes-2026-08-18.md) 里"仍未覆盖"清单，本次未涉及，继续留待后续：

- `sync` 包：`Mutex`/`RWMutex`（读写锁区别）/`WaitGroup`
- `context.WithValue` 的真实代码示例
- `go.mod`/模块系统与依赖管理
- 测试：`_test.go`、`testing` 包、表驱动测试（`mysql_repository_integration_test.go`/`repository_test.go` 已经在项目里，尚未逐行解读）
- 接口进阶：多接口实现、nil 接口 vs 接口包裹 nil 指针的陷阱
- 泛型（`[T any]` 写法，`page.Result[Order]` 已经用到但没深挖）
- 项目整体架构图
