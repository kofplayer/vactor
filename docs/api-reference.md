# vactor API 参考（L2）

> 返回 [vactor/CLAUDE.md](../CLAUDE.md)。调度原理见 [architecture.md](architecture.md)。

`Actor` 本质是一个函数：`type Actor func(EnvelopeContext)`。注册时提供 `func() Actor` 工厂。

## SystemConfig

| 字段 | 默认 | 说明 |
|------|------|------|
| `SystemId` | 0 | 系统标识；单机模式保持 0 |
| `GroupCount` | 0 → NumCPU | actor 分组数 |
| `DefaultStopInterval` | 10min | actor 闲置自动回收时间；0 = 永不回收 |
| `TickInterval` | 1s | tick 周期；**≤0 关闭 tick（见下方警告）** |
| `MailboxHighWaterMark` | 0（不告警） | actor mailbox 深度达到该值记 Warn（只告警不丢弃） |
| `MaxMailboxDepth` | 0（不限制） | actor mailbox 深度上限；达到上限的新消息被丢弃并记 Error（慢消费者背压）。**同时作用于 group mailbox**，其阈值为 `MaxMailboxDepth × GroupMailboxDepthFactor(8)` |
| `OuterQueueMaxDepth` | 0（不限制） | 外部 watch / 事件队列（`System.Watch` / `ListenEvent` 传入的 `*Queue[interface{}]`）深度上限；达上限后丢弃通知、记 Warn 并**摘除该订阅** |
| `LogFunc` | stdout 打印 | 自定义日志（仅 Start 后生效；Start 前的日志走默认 stdout 实现） |

> **背压是"丢弃"而不是"反压调用方"**：达到 `MaxMailboxDepth` 时 `Send` / `BatchSend` 仍返回 `nil`（停机等其他失败场景除外），失败只体现在 Error 日志里。若需要"不丢消息"，应在业务侧做确认/重试，而不是依赖背压。
>
> `MaxMailboxDepth` 覆盖两层：**actor mailbox**（单个慢消费者）与 **group mailbox**（组内全部流量的入口，信封在其中停留极短，故按 8 倍放大，避免把正常突发误判成过载）。只配前者会让人误以为已受保护。
>
> **`OuterQueueMaxDepth` 的摘除是永久性的**：一次溢出，该订阅就没了（这是为了防止无界增长——无界队列永不入队失败，既有的"入队失败即摘除"清理逻辑从未生效过）。请按消费者的最坏停顿时间留出余量，消费者侧应保证持续 `Dequeue`。

> **⚠️ `TickInterval <= 0` 的完整后果**：tick 循环是框架做周期维护的唯一时机，关闭后不只是「不做闲置回收检查」，还会连带停掉 **异步请求超时扫描**——`RequestAsync` 指定了 timeout 也**永远不会触发超时回调**（回调永久悬挂），同时 `processingRequestCount` 无法通过超时路径归零，actor **永不回收**。仅当你的 actor 全部不需要回收、也不使用带超时的异步请求时才可关闭；否则请保留默认 1s。
>
> 若确实想降低 tick 开销，正确做法不是关掉 tick，而是给不需要周期任务的 actor 调 `ctx.SetTickEnabled(false)`（框架仍会为有未完成异步回调、或满足回收条件的 actor 投递 tick）。

## System 接口（[system.go](../system.go)）

| 方法 | 说明 |
|------|------|
| `RegisterActorType(type, creator)` | 注册 actor 类型；`type ≥ ActorTypeStart(10)`；仅 Start 前（重复注册记 Warn，新 creator 覆盖旧的） |
| `Start()` / `Stop()` / `IsRunning()` | 启动（创建 group、ticker；双重 Start 被拒绝）/ 优雅停止（关 mailbox、等 WaitGroup；不可重启）/ 运行态。Start 前发送消息返回 `ErrorCodeSystemNotStarted` |
| `Send(ref, msg)` | 单向消息，无返回 |
| `Request(ref, msg, timeout) (interface{}, VAError)` | 系统外同步请求；`timeout ≤ 0` 表示无限等待——**会一直阻塞调用方 goroutine 直到目标响应**，目标不响应则永久悬挂，建议显式给时限 |
| `Watch(ref, watchType, queue)` / `Unwatch(...)` | 系统外 watch，通知投递到 `Queue[interface{}]`（消息为 `*MsgOnWatchMsg`） |
| `ListenEvent(group, id, queue)` / `UnlistenEvent` / `FireEvent(group, id, msg)` | 系统外事件订阅/触发；队列收到 `*MsgOnEventMsg` |
| `BatchSend(refs, msgs) VAError` | 批量发送；所有 ref 收到全部 msgs 的逐条副本 |
| `CreateActorRef(type, id)` / `CreateActorRefEx(systemId, type, id)` | 创建引用；`CreateActorRef` 等价于 `CreateActorRefEx(0, ...)`。**vactor 单机版下 SystemId 恒为 0，只按 ActorId 算出 GroupSlot**；"按 ActorId 哈希选节点"是 dvactor 替换寻址函数后的行为 |
| `SetRouter(router)` / `LocalRouter(envelope)` | 分布式扩展钩子：替换路由 / 本地默认路由（扩展机制见架构文档） |
| `SetCreateActorRefExFunc(f)` | 分布式扩展钩子：替换寻址函数 |
| `LogDebug/Info/Warn/Error/Fatal/Panic` | Logger；Panic 级会 panic |

## EnvelopeContext 接口（[envelope_context.go](../envelope_context.go)）

actor 内可用，包含 Logger 全部方法，另有：

| 方法 | 说明 |
|------|------|
| `GetActorRef()` / `GetFromActorRef()` / `GetMessage()` | 当前 actor / 发送方（系统外发起为 nil）/ 消息体 |
| `Send(ref, msg)` | actor 间单向消息（From 自动带自己） |
| `RequestAsync(ref, msg, timeout, callback)` | 异步请求；回调在本 actor goroutine 执行；`timeout ≤ 0` 不超时 |
| `Request(ref, msg, timeout) (interface{}, VAError)` | **同步**请求，阻塞本 actor goroutine（其他 actor 不受影响）。**向自身发起会立即返回 `ErrorCodeSelfRequest`**（见下方说明） |
| `Response(msg, err)` | 回应 Request；仅一次有效；非 Request 消息调用只记错误日志 |
| `Watch(ref, watchType)` / `Unwatch` | actor 间订阅；通知以 `*MsgOnWatchMsg` 消息送达 |
| `Notify(watchType, msg)` | 向本 actor 的所有 watcher（内部+外部）广播 |
| `ListenEvent` / `UnlistenEvent` / `FireEvent` | actor 内事件订阅/触发；事件以 `*MsgOnEventMsg` 送达 |
| `BatchSend(refs, msgs)` | 同 System.BatchSend |
| `SetTickEnabled(enabled)` | 声明是否需要周期性 `MsgOnTick`；默认 true。关闭后仅在有未完成异步回调、或闲置回收条件已满足时才投递 tick（纯空闲 actor 不再被每秒唤醒） |
| `SetStopInterval(d)` | 覆盖本 actor 的闲置回收时间；0 = 永不回收 |
| `SetSelfInvalid()` | 自我失效：拒收后续消息、Request 立即回错、1s 后回收（生命周期见架构文档） |
| `CreateActorRef` / `CreateActorRefEx` | 同 System |
| `LocalRouter(envelope)` | 把信封直接交给本地路由（代理类 actor 用，dvactor 的 WatchProxy 即如此） |

> **⚠️ 不要向自身发起同步 `Request`**：`EnvelopeRequest` 走的是 mailbox，而调用方 goroutine 此刻正阻塞在等待响应上，处理不到自己发出的那条请求——必然超时；`timeout ≤ 0` 时更是**永久挂死该 actor**（此后它既不能处理任何消息，也不会被闲置回收）。框架因此直接返回 `ErrorCodeSelfRequest` 而不进入等待。
>
> 需要自发请求时用 `RequestAsync`——异步路径不阻塞调用方，响应回来后回调照常执行。

## 内置消息（[message.go](../message.go)）

| 消息 | 时机 |
|------|------|
| `*MsgOnStart` | actor goroutine 启动后第一条 |
| `*MsgOnStop` | actor 回收前最后一条 |
| `*MsgOnTick` | 每个 TickInterval 一条（未触发回收时）；不需要周期任务的 actor 可用 `SetTickEnabled(false)` 声明，避免被无谓唤醒 |
| `*MsgOnWatchMsg` | watch 通知：含 `ActorRef`（被观察者）、`WatchType`、`Message` |
| `*MsgOnEventMsg` | 事件通知：含 `EventGroup`、`EventId`、`Message` |

## 核心类型与常量（[actor.go](../actor.go)）

```go
type SystemId uint16; type GroupSlot uint16
type ActorType uint32; type ActorId string
type WatchType uint32; type EventGroup string; type EventId uint32
const EventHubActorType ActorType = 1   // 事件总线保留
const ActorTypeStart    ActorType = 10  // 业务类型下限
```

`ActorRef` 接口：`GetActorType/GetActorId/GetSystemId/GetGroupSlot`；默认实现 `ActorRefImpl`（结构体值可作 map key）。

`HashActorId(actorId) uint32`（[actor.go](../actor.go)）：ActorId 的 32 位 **FNV-1a** 哈希。单机取低 16 位作 `GroupSlot`（0 归一到 1）；dvactor 用 `hash % 节点数` 选放置节点、再用商做分片——**两处共用同一函数**，改动它会同时改变本机落组与跨节点放置（属跨版本行为契约，详见[架构文档](architecture.md)）。

## 错误（[error.go](../error.go)）

`VAError` = `error` + `Code() ErrorCode`。内置：`ErrorCodeSuccess(0)`、`ErrorCodeTimeout(1)`、`ErrorCodeInvalidActor(2)`、`ErrorCodeSystemNotStarted(3)`、`ErrorCodeHandlerPanic(4)`、`ErrorCodeSelfRequest(5)`；业务自定义从 `ErrorCodeCustomStart(100)` 起。`Error()` 返回 `VaError(code=N)`，判错应比较 `Code()`。dvactor 侧码表见 [cluster.md](../../dvactor/docs/cluster.md)。
