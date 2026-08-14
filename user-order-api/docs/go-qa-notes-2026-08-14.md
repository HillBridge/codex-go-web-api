# Go 学习问答笔记（2026-08-14）

本文档整理自 2026-08-14 的学习问答，主题聚焦 goroutine 泄漏的成因、Go 高并发能力的底层支撑、单机并发容量的瓶颈判断，以及 `select` 多路复用机制。

## 一、goroutine 泄漏

### "永久阻塞会导致 goroutine 泄漏"是什么意思

- 一个 goroutine 如果卡在等待 channel（比如 `<-ch`），但**永远没有其他代码往这个 channel 写数据**，它就会永远停在这一行，无法往下执行也无法退出
- Go runtime 不会自动清理"卡住的" goroutine（不像局部变量会被 GC 回收），只要程序不退出，这个 goroutine 就一直占着内存和调度资源
- 这种"永远存在但什么都不做、还占资源"的 goroutine 就是 **goroutine 泄漏**，类似其他语言的内存泄漏

```go
func leak() {
    ch := make(chan int)
    go func() {
        val := <-ch // 永远没人往 ch 写数据，这里永久阻塞
        fmt.Println(val)
    }()
    // 函数返回了，但这个 goroutine 还卡在 <-ch，永远退不出去
}
```

若 `leak()` 被频繁调用（如每次请求调一次），泄漏的 goroutine 会越积越多，最终耗尽内存或调度资源。

### 为什么会出现"等一个永远没人写入的 channel"这种场景

不是故意写出的 bug，通常是正常逻辑分支或异常路径下"意外"出现的：

1. **生产者提前返回/panic，没走到写 channel 那一步**——业务分支变多后容易漏掉某条路径上的写入
2. **反过来：consumer 提前退出（timeout/cancel），producer 卡在写 channel 上**——同样是泄漏，只是角色对调
3. **fan-out/fan-in 模式里，只有一条路径会关闭/写入共享 channel**——某个 worker 异常退出（如 panic 被上层 recover）没走到收尾的 `close(ch)`，等待方永久卡住
4. **重试/超时逻辑写漏了，天真地假设"对方一定会写"**，没加 `select` 兜底

核心原因：channel 通信要求"收发成对"，但 Go 编译器不检查这种成对关系在所有代码路径下是否成立——尤其是错误处理、超时、cancel 这些"非主流程"分支最容易漏掉对应的读或写。规范做法是凡是从 channel 收发都配合 `select` + `ctx.Done()`/超时，不要假设对方一定会来。

## 二、Go 如何做到高并发

1. **goroutine 轻量**：初始栈 2KB，按需增长/收缩，创建/切换成本远低于 OS 线程
2. **GMP 调度模型**：G（goroutine）/M（OS 线程）/P（逻辑处理器，默认数量=CPU 核数）。调度器把大量 G 多路复用到少量 M 上，用 **work stealing** 让空闲 P 去别的 P 那里偷任务
3. **非阻塞 I/O + netpoller**：goroutine 做网络 I/O 时被挂起、不占用 M，底层用 epoll/kqueue/IOCP 监控；数据就绪后 netpoller 唤醒对应 G 重新入队。因此"一万个 goroutine 等网络"不需要一万个 OS 线程
4. **抢占式调度**（Go 1.14+）：基于信号的异步抢占，避免死循环的 goroutine 拖垮整个 P
5. **channel + CSP 并发模型**：channel 是一等公民，配合 `select` 做多路复用，比"共享内存+锁"更易推理
6. **并发、低延迟 GC**：三色标记 + 写屏障，标记阶段与业务代码并发执行，STW 压缩到微秒级
7. **sync/atomic 原语**：`Mutex`/`RWMutex`/`WaitGroup`/`Once`/`atomic`，与 channel 互补，供确实需要共享状态的场景使用

一句话：把"并发"从 OS 线程的重量级模型下放到 runtime 自管的轻量 goroutine + 高效调度器（GMP）+ 非阻塞 I/O（netpoller），再用 channel/select 简化通信逻辑。

## 三、单机支撑数十万并发需要几核

**核心结论：核数和"能支撑多少并发连接"关系不大，4-8 核就能撑住数十万甚至上百万连接。**

先区分两个概念：
- **并发数**：有多少个 goroutine/连接同时"活着"
- **并行数（核数决定）**：同一时刻能真正并行执行代码的 CPU 数量

原因：
- `GOMAXPROCS`（默认=核数）决定的是同一时刻能有多少 M 在真正跑 Go 代码，不是能存在多少 goroutine
- 数十万 goroutine 中大部分时间在等网络 I/O，处于挂起状态，不占用 M/P，只占一点内存
- 真正需要"并行"的只是那一小部分同时在算的 goroutine，数量远小于总连接数，几个核就能轮流调度过来

**真正的瓶颈通常是：**
1. **内存**——每连接的栈+buffer，10 万连接可能占几百 MB 到几 GB，往往比 CPU 先见顶
2. **文件描述符上限**（`ulimit -n`）——每个 TCP 连接是一个 fd，默认 1024 远不够
3. **网络带宽/PPS**
4. **GC 压力**（但 Go 并发 GC 能把这部分压得较低）

**核数才是瓶颈的场景**：业务是 CPU 密集型（大量计算/加密/序列化），此时同时能并行处理的请求数直接受核数限制——这是"数十万并发连接"和"数十万 QPS 的计算密集型请求"的本质区别，不能混为一谈。

## 四、select 多路复用

### select 就是 channel 的多路复用机制

类似 Unix 的 `select`/`epoll`：同时监听多个 I/O 源，谁先就绪就处理谁。

```go
select {
case v1 := <-ch1:
    fmt.Println("ch1 收到:", v1)
case v2 := <-ch2:
    fmt.Println("ch2 收到:", v2)
case <-ctx.Done():
    fmt.Println("超时或取消")
}
```

### 如何区分该进哪个 case

**case 和 channel 的对应关系是写代码时就写死的，不是运行时去"猜"的**——每个 `case` 明确写了对哪个 channel 做什么操作（如 `<-ch1` 就是"从 ch1 读"），不存在运行时"识别是哪个 channel"的问题。

`select` 真正要判断的是："这些 case 里，哪些现在可以立即执行"：

1. 同时检查所有 case 对应的 channel 操作是否"就绪"（`<-ch1` 就绪指 ch1 有数据可读；`ch2 <- x` 就绪指 ch2 还有空间可写）
2. 只有一个 case 就绪 → 执行它
3. 多个 case 同时就绪 → **伪随机**选一个执行（不按代码顺序），避免某个 channel 被长期饿死
4. 没有 case 就绪：有 `default` → 立即执行 `default` 不阻塞；没有 `default` → 阻塞直到某个 channel 就绪

典型多路复用场景——一个 goroutine 同时"看着"多个信号源：

```go
func worker(ctx context.Context, dataCh chan int, cancelCh chan struct{}) {
    for {
        select {
        case data := <-dataCh:
            process(data)
        case <-cancelCh:
            return
        case <-ctx.Done():
            return
        }
    }
}
```

价值：一个 goroutine 不用为每个 channel 单独开一个阻塞等待，用一个 `select` 统一调度。
