# vactor — 虚拟 actor 框架（L1）

高性能、轻量级虚拟 actor 框架，**零第三方依赖**（`go.mod` 仅有 module 声明，Go 1.24）。

## 设计核心

- Actor 逻辑上**永远存在**，不能显式创建/销毁。
- 向未激活的 actor 发消息时系统自动创建（按需激活）。
- actor 闲置超时（可配置，默认 10 分钟）后自动回收，watcher 订阅关系通过 cache 保留。
- 调度单位是 **actorGroup**（默认数量 = CPU 核数），每个 group 一个 goroutine + 一个 mailbox；actor 按 `GroupSlot`（由 ActorId 哈希）固定落到某个 group，保证同 actor 消息串行。

## 文件地图

| 文件 | 内容 |
|------|------|
| [actor.go](actor.go) | 核心类型：`Actor`（本质是 `func(EnvelopeContext)`）、`ActorRef`/`ActorRefImpl`、`ActorType`/`ActorId`/`SystemId`/`GroupSlot`、`Logger`、`Router`、常量 |
| [system.go](system.go) | `System` 接口与实现、`SystemConfig`、本地路由 `LocalRouter`、事件接口、ActorRef 创建与哈希 |
| [actor_context.go](actor_context.go) | `actorContext`：actor 消息循环、生命周期（start/stop/tick）、watcher 缓存、Request/RequestAsync 实现 |
| [envelope.go](envelope.go) | 全部 `Envelope` 信封类型（Send/BatchSend/Request/Response/Watch/Notify/FireNotify/Outer*） |
| [envelope_context.go](envelope_context.go) | `EnvelopeContext` 接口（actor 内可用的全部能力）及各信封对应的上下文实现 |
| [group.go](group.go) | `actorGroup`：actor 分片调度、mailbox 创建/回收、actor 停止后的 cache 处理 |
| [message.go](message.go) | 内置消息：`MsgOnStart`/`MsgOnStop`/`MsgOnTick`/`MsgOnWatchMsg`/`MsgOnEventMsg` |
| [queue.go](queue.go) | 泛型阻塞队列 `Queue[T]`（cond + ring buffer），外部 watch/event 也用它收通知 |
| [ring_buffer.go](ring_buffer.go) | 泛型环形缓冲 `RingBuffer[T]`，满时自动 ×2 扩容 |
| [error.go](error.go) | `VAError`/`ErrorCode`；自定义错误码从 `ErrorCodeCustomStart(100)` 起 |

## 关键约束与陷阱

- **ActorType 必须 ≥ `ActorTypeStart`(10)**；`EventHubActorType`(1) 是事件总线保留类型。事件本质是挂在 EventHub actor 上的 watch，同 `EventGroup` 内严格有序。
- `RegisterActorType`、`SetRouter`、`SetCreateActorRefExFunc` 只能在 `Start()` 之前调用。
- `ctx.Response()` 仅对 Request 类消息有效，且**只能调用一次**；对 Send/Notify 调用会记错误日志。
- `SetSelfInvalid()` 使 actor 拒收后续消息（Request 会收到 `ErrorCodeInvalidActor`），1 秒后回收，之后可再次激活。
- `SystemId=0` 表示"未指定"，本地单机模式下所有 actor 都属于本系统。
- 分布式扩展点：`SetRouter`（替换路由）与 `SetCreateActorRefExFunc`（替换寻址），dvactor 即通过这两个钩子接入——见 [dvactor/CLAUDE.md](../dvactor/CLAUDE.md)。

## 示例（[examples/](examples/)）

hello（最小用法）· send · [request](examples/request/main.go)（内/外同步异步请求）· event · [watch](examples/watch/main.go)（内/外 watch）· lifecycle · invalidactor · [benchmark](examples/benchmark/main.go)（i5-13400F：1 亿消息 / 1 万 actor ≈ 4.8s）

## 深入阅读（L2）

- [docs/architecture.md](docs/architecture.md) — 调度模型、消息流、生命周期
- [docs/api-reference.md](docs/api-reference.md) — System / EnvelopeContext 完整 API
- [docs/internals.md](docs/internals.md) — Envelope 家族、watch 缓存、队列实现、错误码

用户文档：[Readme.md](Readme.md)（EN）· [ReadmeCh.md](ReadmeCh.md)（中文）
