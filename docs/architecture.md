# vactor 架构（L2）

> 返回 [vactor/CLAUDE.md](../CLAUDE.md)。API 细节见 [api-reference.md](api-reference.md)，实现细节见 [internals.md](internals.md)。

## 分层结构

```
System（1 个进程 1 个）
 └── actorGroup[N]（N = GroupCount，默认 runtime.NumCPU()）
      ├── group mailbox（Queue[Envelope]，group 自己的 goroutine 消费）
      └── actorContext[M]（按 GroupSlot 落组，每个 actor 一个 goroutine + 一个 mailbox）
```

- `System`（[system.go](../system.go)）：持有所有 group、配置、router、logFunc。
- `actorGroup`（[group.go](../group.go)）：调度分片。消费 group mailbox，按 `ActorRefImpl` 查找/创建 actor 的 mailbox 与 context，把信封投入 actor mailbox。
- `actorContext`（[actor_context.go](../actor_context.go)）：actor 的运行实体。一个 goroutine 循环 `DequeueAll` 批量取消息处理。

## 消息流（Send 为例）

```
system.Send(ref, msg)
  → sendEnvelope(&EnvelopeSend{...})
  → router(envelope)              // 默认 LocalRouter；dvactor 会替换为集群路由
  → LocalRouter: group = groups[(ref.GroupSlot-1) % groupCount]
  → group.mailbox.Enqueue
  → group goroutine: processEnvelope
      ├─ actor 不存在 → 创建 mailbox + actorContext（附带已保存的 watcher cache）→ ctx.start()
      └─ actorMailbox.Enqueue(envelope)
  → actor goroutine: DequeueAll → 逐条构造 EnvelopeContext → onMessage(ctx)
```

BatchSend/Notify（多收件人）先在 LocalRouter 按 group 拆分投递，group 内再按 actor 拆分。

## GroupSlot 哈希（寻址分片）

`defaultCreateActorRefEx`（`system.go`）：对 ActorId 字符串**从尾部向前**逐字节做 2 路交替 XOR，得到 16bit 值作为 `GroupSlot`（为 0 时置 1）。落组公式：`(GroupSlot-1) % groupCount`。这保证同一 actor 的消息永远进同一个 group 的 goroutine，天然串行。

## 生命周期

1. **激活**：group 处理信封时发现 actor 无 context → 创建 → goroutine 启动 → 先收到 `MsgOnStart`。
2. **运行**：每收到一条非 tick 消息刷新 `latestMsgTime`。
3. **Tick**：System 的 ticker（默认 1s）向每个 group mailbox 投一个 `envelopeTick`，group 逐个 actor 判定后再扇出。actor 处理 tick 时检查：
   - 若 `processingRequestCount <= 0`、无待处理异步回调、`stopInterval > 0` 且闲置超时 → goroutine 退出（回收）。
   - 同时处理超时的 RequestAsync 回调（回调收到 `ErrorCodeTimeout`）。
   - 否则向 actor 投递 `MsgOnTick`（可在 actor 内做周期任务，如示例中用 `ctx.Notify` 推 watch）。

   > **扇出优化**：group 先用无锁原子字段粗筛（`actorContext.needTick`），只向"确实需要"的 actor 投递——未声明关闭 tick 的 actor、存在未完成异步回调的 actor、以及闲置回收条件已满足的 actor。声明 `SetTickEnabled(false)` 的纯空闲 actor 不再每秒被入队与唤醒（2 万 actor 的 tick 扇出开销约 30.8ms → 11.1ms）。
4. **回收**：goroutine 退出前收到 `MsgOnStop`，然后向 group mailbox 投 `envelopeStopedReport`；group 将 watcher cache 存入 `actorCaches`，若 mailbox 中还有积压消息则立即重建 context 继续处理。
5. **SetSelfInvalid**：置 `isInvalid`，此后所有消息被拒（Request 类立即回 `ErrorCodeInvalidActor`），stopInterval 缩为 1 秒触发快速回收；之后仍可被新消息重新激活。

## 并发模型要点

- actor 的 `onMessage` 在**单 goroutine** 内串行执行 → actor 内部状态无需加锁。
- 每个 actor 独占 goroutine（非共享线程池），`ctx.Request`（同步）只是阻塞本 actor 的 goroutine，不影响其他 actor。
- 同步 Request 的响应经 `syncRspChan` 直达 actor goroutine，不进 mailbox；匹配与迟到丢弃机制见 [internals.md](internals.md)。
- `processMessage` 与批量处理均带 recover：actor 内 panic 只记错误日志（请求类消息自动回 `ErrorCodeHandlerPanic`），异常信封不会拖垮 actor、group 或进程。`ctx.LogPanic` 记日志后 panic，同样被批量 recover 捕获。

## 事件总线

`EventHubActorType`(1) 是一个特殊 actor 类型，其 creator 返回 nil（不处理消息）。`ListenEvent(g, id)` = 对 `CreateActorRef(EventHubActorType, ActorId(g))` 做 `Watch(id)`；`FireEvent` 向该 actor 投 `EnvelopeFireNotify` 触发 notify 扇出。同 EventGroup 的事件落在同一 actor 上 → 严格有序。
