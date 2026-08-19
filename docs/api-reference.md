# vactor API 参考（L2）

> 返回 [vactor/CLAUDE.md](../CLAUDE.md)。调度原理见 [architecture.md](architecture.md)。

`Actor` 本质是一个函数：`type Actor func(EnvelopeContext)`。注册时提供 `func() Actor` 工厂。

## SystemConfig

| 字段 | 默认 | 说明 |
|------|------|------|
| `SystemId` | 0 | 系统标识；单机模式保持 0 |
| `GroupCount` | 0 → NumCPU | actor 分组数 |
| `DefaultStopInterval` | 10min | actor 闲置自动回收时间；0 = 永不回收 |
| `TickInterval` | 1s | tick 周期；≤0 关闭 tick（同时关闭闲置回收检查） |
| `LogFunc` | stdout 打印 | 自定义日志 |

## System 接口（[system.go](../system.go)）

| 方法 | 说明 |
|------|------|
| `RegisterActorType(type, creator)` | 注册 actor 类型；`type ≥ ActorTypeStart(10)`；仅 Start 前 |
| `Start()` / `Stop()` / `IsRunning()` | 启动（创建 group、ticker）/ 优雅停止（关 mailbox、等 WaitGroup）/ 运行态 |
| `Send(ref, msg)` | 单向消息，无返回 |
| `Request(ref, msg, timeout) (interface{}, VAError)` | 系统外同步请求；`timeout ≤ 0` 表示无限等待 |
| `Watch(ref, watchType, queue)` / `Unwatch(...)` | 系统外 watch，通知投递到 `Queue[interface{}]`（消息为 `*MsgOnWatchMsg`） |
| `ListenEvent(group, id, queue)` / `UnlistenEvent` / `FireEvent(group, id, msg)` | 系统外事件订阅/触发；队列收到 `*MsgOnEventMsg` |
| `BatchSend(refs, msgs) VAError` | 批量发送；所有 ref 收到全部 msgs 的逐条副本 |
| `CreateActorRef(type, id)` / `CreateActorRefEx(systemId, type, id)` | 创建引用；前者 SystemId=0（自动计算） |
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
| `Request(ref, msg, timeout) (interface{}, VAError)` | **同步**请求，阻塞本 actor goroutine（其他 actor 不受影响） |
| `Response(msg, err)` | 回应 Request；仅一次有效；非 Request 消息调用只记错误日志 |
| `Watch(ref, watchType)` / `Unwatch` | actor 间订阅；通知以 `*MsgOnWatchMsg` 消息送达 |
| `Notify(watchType, msg)` | 向本 actor 的所有 watcher（内部+外部）广播 |
| `ListenEvent` / `UnlistenEvent` / `FireEvent` | actor 内事件订阅/触发；事件以 `*MsgOnEventMsg` 送达 |
| `BatchSend(refs, msgs)` | 同 System.BatchSend |
| `SetStopInterval(d)` | 覆盖本 actor 的闲置回收时间；0 = 永不回收 |
| `SetSelfInvalid()` | 自我失效：拒收后续消息、Request 立即回错、1s 后回收（生命周期见架构文档） |
| `CreateActorRef` / `CreateActorRefEx` | 同 System |
| `LocalRouter(envelope)` | 把信封直接交给本地路由（代理类 actor 用，dvactor 的 WatchProxy 即如此） |

## 内置消息（[message.go](../message.go)）

| 消息 | 时机 |
|------|------|
| `*MsgOnStart` | actor goroutine 启动后第一条 |
| `*MsgOnStop` | actor 回收前最后一条 |
| `*MsgOnTick` | 每个 TickInterval 一条（未触发回收时） |
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

## 错误（[error.go](../error.go)）

`VAError` = `error` + `Code() ErrorCode`。内置：`ErrorCodeSuccess(0)`、`ErrorCodeTimeout(1)`、`ErrorCodeInvalidActor(2)`；业务自定义从 `ErrorCodeCustomStart(100)` 起。注意 `vaError.Error()` 固定返回 `"VaError"`，判错应比较 `Code()`。
