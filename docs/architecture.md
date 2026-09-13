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

`HashActorId`（`actor.go`）：对 ActorId 做 32 位 **FNV-1a** 哈希。`defaultCreateActorRefEx`（`system.go`）取该哈希的**低 16 位**作为 `GroupSlot`（为 0 时置 1），落组公式 `(GroupSlot-1) % groupCount`——同一 actor 的消息因此永远进同一个 group 的 goroutine，天然串行。dvactor 复用同一函数：用 `hash % 节点数` 选放置节点，再用商做分片。

> **为什么不用更省事的交替 XOR**：vactor 与 dvactor 早期实现都是"从尾部向前、按位置交替 XOR 进 2/4 个字节桶"。实测（2 万个结构化 id、16 个 group）它把 `user1..user20000` 压进仅 **762** 个槽位（96% 的 id 与其他 id 撞槽），最重桶达最轻桶的 2.5 倍（变异系数 0.37）；`room-N-player-M` 更差（0.60）。而结构化 id 恰是业务常态。改用 FNV-1a 后唯一槽位 17586、变异系数 0.006。
>
> **升级注意**：哈希一变，同一 actor 的本机落组与**跨节点放置**都会改变。集群必须**整体停机升级，不可滚动升级**——否则新旧节点对同一 actor 的放置结论不一致，可能同时存在两个实例。

## 生命周期

1. **激活**：group 处理信封时发现 actor 无 context → 创建 → goroutine 启动 → 先收到 `MsgOnStart`。
2. **运行**：每收到一条非 tick 消息刷新 `latestMsgTime`。
3. **Tick**：System 的 ticker（默认 1s）向每个 group mailbox 投一个 `envelopeTick`，group 逐个 actor 判定后再扇出。actor 处理 tick 时检查：
   - **回收需同时满足 5 个条件**：本条 tick 是**本批最后一条消息**（`n == len(msgs)-1`）、`processingRequestCount <= 0`、无待处理异步回调、`stopInterval > 0`、且闲置已超时（`latestMsgTime + stopInterval < now`）→ goroutine 退出（回收）。
   - 同时处理超时的 RequestAsync 回调（回调收到 `ErrorCodeTimeout`）。
   - 否则向 actor 投递 `MsgOnTick`（可在 actor 内做周期任务，如示例中用 `ctx.Notify` 推 watch）。

   > **扇出优化**：group 先用无锁原子字段粗筛（`actorContext.needTick`），只向"确实需要"的 actor 投递——未声明关闭 tick 的 actor、存在未完成异步回调的 actor、以及闲置回收条件已满足的 actor。声明 `SetTickEnabled(false)` 的纯空闲 actor 不再每秒被入队与唤醒（2 万 actor 的 tick 扇出开销约 30.8ms → 11.1ms）。
4. **回收**：goroutine 退出前收到 `MsgOnStop`，然后向 group mailbox 投 `envelopeStopedReport`；group 将 watcher cache 存入 `actorCaches`，若 mailbox 中还有积压消息则立即重建 context 继续处理。
5. **SetSelfInvalid**：置 `isInvalid`，此后所有消息被拒（Request 类立即回 `ErrorCodeInvalidActor`），stopInterval 缩为 1 秒触发快速回收；之后仍可被新消息重新激活。

## 并发模型要点

- actor 的 `onMessage` 在**单 goroutine** 内串行执行 → actor 内部状态无需加锁。
- 每个 actor 独占 goroutine（非共享线程池），`ctx.Request`（同步）只是阻塞本 actor 的 goroutine，不影响其他 actor。
- **规模建议**：goroutine 初始栈约 2KB（随调用深度增长），内存开销随**同时活跃**的 actor 数线性增长——10 万活跃 actor 对应数百 MB 级栈开销。建议把峰值活跃 actor 控制在 10 万量级；历史上创建过多少 actor 不重要，闲置的会被回收。
- 同步 Request 的响应经 `syncRspChan` 直达 actor goroutine，不进 mailbox；匹配与迟到丢弃机制见 [internals.md](internals.md)。
- `processMessage` 与批量处理均带 recover：actor 内 panic 只记错误日志（请求类消息自动回 `ErrorCodeHandlerPanic`），异常信封不会拖垮 actor、group 或进程。`ctx.LogPanic` 记日志后 panic，同样被批量 recover 捕获。

## 事件总线

`EventHubActorType`(1) 是一个特殊 actor 类型，其 creator 返回 nil（不处理消息）。`ListenEvent(g, id)` = 对 `CreateActorRef(EventHubActorType, ActorId(g))` 做 `Watch(id)`；`FireEvent` 向该 actor 投 `EnvelopeFireNotify` 触发 notify 扇出。同 EventGroup 的事件落在同一 actor 上 → 严格有序。
