# vactor — 虚拟 actor 框架（L1）

高性能、轻量级虚拟 actor 框架，**零第三方依赖**（`go.mod` 仅有 module 声明，Go 1.24）。

## 设计核心

- Actor 逻辑上**永远存在**，不能显式创建/销毁；向未激活的 actor 发消息时按需激活。
- actor 闲置超时（可配置）后自动回收，watcher 订阅关系通过 cache 保留。
- 调度单位是 **actorGroup**，每个 group 一个 goroutine + 一个 mailbox；actor 按 `GroupSlot`（由 ActorId 哈希）固定落到某个 group，保证同 actor 消息串行。（各项默认值见 [API 参考](docs/api-reference.md)）

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
| [error.go](error.go) | `VAError`/`ErrorCode`（码表见 [API 参考](docs/api-reference.md)） |

## 关键约束与陷阱

- **ActorType 必须 ≥ `ActorTypeStart`(10)**；`EventHubActorType`(1) 是事件总线保留类型，同 `EventGroup` 事件严格有序（机制见 [架构文档](docs/architecture.md)）。
- `RegisterActorType`、`SetRouter`、`SetCreateActorRefExFunc` 只能在 `Start()` 之前调用（Stop 后同样被拒绝）；未 Start 发消息返回 `ErrorCodeSystemNotStarted`；双重 `Start` 被拒绝；`Stop` 后不可重启。
- panic 防护：actor 与 group 的信封处理整批 recover，用户 panic（含 `ctx.LogPanic`、异步回调 panic）只记日志；请求类消息 panic 且未 `Response` 时框架代为回错（语义见 [架构文档](docs/architecture.md)）。
- `ctx.Response()` 仅对 Request 类消息有效，且**只能调用一次**；对 Send/Notify 调用会记错误日志。
- **不要向自身发起同步 `ctx.Request`**：请求信封进的是 mailbox，而调用方 goroutine 正阻塞等待响应，永远处理不到它——必然超时，`timeout<=0` 时永久挂死该 actor。框架直接返回 `ErrorCodeSelfRequest`(5)（详见 [API 参考](docs/api-reference.md)）；自发请求请用 `ctx.RequestAsync`。
- **背压是"丢弃"而非"反压调用方"**：`MaxMailboxDepth` 达上限时 `Send`/`BatchSend` 仍返回 nil，失败只在 Error 日志里。它覆盖 actor mailbox 与 **group mailbox** 两层（后者阈值为 `MaxMailboxDepth × GroupMailboxDepthFactor(8)`）。`OuterQueueMaxDepth` 达上限会**永久摘除**该外部订阅，需按消费者最坏停顿留足余量。
- `SystemId=0` 表示"未指定"，本地单机模式下所有 actor 都属于本系统。
- **SystemId 越界（单机版）**：`System` 内部的 `systemId` 恒为 0。若用 `CreateActorRefEx(非0, ...)` 造出 SystemId≠0 的引用并投递，`group.processEnvelope` 会 `LogPanic`——panic 被 group 的整批 recover 捕获，**该批消息全部静默丢弃**（进程不崩，但消息丢失且只有一行日志）。除非像 dvactor 那样用 `SetCreateActorRefExFunc` 接管寻址，不要手工指定非 0 的 SystemId。
- 分布式扩展点：`SetRouter`（替换路由）与 `SetCreateActorRefExFunc`（替换寻址），dvactor 即通过这两个钩子接入——见 [dvactor/CLAUDE.md](../dvactor/CLAUDE.md)。

## 示例（[examples/](examples/)）

hello（最小用法）· send · [request](examples/request/main.go)（内/外同步异步请求）· event · [watch](examples/watch/main.go)（内/外 watch）· lifecycle · invalidactor · [benchmark](examples/benchmark/main.go)（i5-13400F：1 亿消息 / 1 万 actor ≈ 4.8s）

## 测试

`go test ./...` 覆盖队列/环形缓冲、生命周期与回收缓存、消息与批量语义、同步异步请求、watch/event、并发顺序与 panic 防护。测试辅助工具在 [testutil/](testutil/testutil.go)（`NewSystem`、`Collector`、`WaitFor/WaitChan/NoReceive`、`FreePorts`），dvactor 的测试同样复用。

**基准**：标准基准在 [bench_test.go](bench_test.go)（同步/异步请求往返、发送吞吐）与 [tick_bench_test.go](tick_bench_test.go)（tick 扇出对比）。本地对比用 `go test -run '^$' -bench . -benchmem ./`；CI 只做冒烟（`-benchtime 1x`），不设性能阈值。

## 深入阅读（L2）

- [docs/architecture.md](docs/architecture.md) — 调度模型、消息流、生命周期、并发与 panic 防护语义
- [docs/api-reference.md](docs/api-reference.md) — System / EnvelopeContext 完整 API、错误码表
- [docs/internals.md](docs/internals.md) — Envelope 家族、处理循环、watch 缓存、队列实现

用户文档：[Readme.md](Readme.md)（EN）· [ReadmeCh.md](ReadmeCh.md)（中文）
