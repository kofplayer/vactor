# vactor 内部机制（L2）

> 返回 [vactor/CLAUDE.md](../CLAUDE.md)。宏观流程见 [architecture.md](architecture.md)。

## Envelope 家族（[envelope.go](../envelope.go)）

所有内部流转的消息都封装为 `Envelope`（仅要求 `GetToActorRef()`）：

| Envelope | 用途 | 生产者 |
|----------|------|--------|
| `EnvelopeSend` | actor 间单向消息 | ctx.Send / system.Send |
| `EnvelopeBatchSend` | 多收件人批量（`ToActorRefs`+`Messages`） | BatchSend |
| `EnvelopeRequest` / `EnvelopeResponse` | actor 间同步请求/响应 | ctx.Request / ctx.Response |
| `EnvelopeRequestAsync` / `EnvelopeResponseAsync` | actor 间异步请求/响应，带 `CallbackId`+`CallbackAddress` | ctx.RequestAsync / ctx.Response |
| `EnvelopeOuterRequest` | 系统外请求，带 `RspChan` | system.Request |
| `EnvelopeWatch` / `EnvelopeOuterWatch` | actor 内/外 watch 与 unwatch（`IsWatch` 区分） | Watch/Unwatch |
| `EnvelopeNotify` | 向 watcher 扇出通知（多收件人，`Message` 固定为 `*MsgOnWatchMsg`） | actorContext.notify |
| `EnvelopeFireNotify` | 触发通知/事件 | Notify/FireEvent |
| `envelopeTick` / `envelopeStopedReport` | 内部：tick 与 actor 停止上报（小写，不可序列化出包） | system / actorContext |

## actorContext 处理循环（[actor_context.go](../actor_context.go) `start()`）

关键点：

- 每轮 `DequeueAll` 批量取走全部积压消息后逐条处理，取出即置 nil 帮助 GC。
- `EnvelopeWatch`/`EnvelopeOuterWatch` 不进 `onMessage`，直接更新 watcher 表（保证先于业务消息生效）。
- `EnvelopeResponseAsync` 也不进 `onMessage`：按 `CallbackId` 找到回调直接执行，并用 `CallbackAddress`（actorContext 指针地址）校验——防止 actor 回收重建后旧响应打到新实例。
- `processeingRequestCount` 统计未完成的入向 Request（Request/RequestAsync），未归零前 actor 不会因闲置回收（避免同步响应丢失）。
- 同步 `Request` 的响应匹配由 actor goroutine 自己完成：group 只把带 `RequestId` 的 `EnvelopeResponse` 塞进 `syncRspChan`（不读 actor 状态，无跨 goroutine 共享变量），actor 侧比对 `waitingSyncRequestId`，不匹配的迟到响应直接丢弃。该字段因此只是普通 `uint32`。

## Watcher 缓存与 actor 复活

`actorContextCache` 保存两张表：`watcherss`（actor watcher）与 `outerWatcherss`（外部队列 watcher），均按 `WatchType` 分组。

- actor 回收时，若两表非空则 cache 存入 `actorGroup.actorCaches`（[group.go](../group.go) `onActorStoped`）。
- 同 actor 再次激活时取回 cache → **watcher 关系在 actor 回收后仍然保留**。
- `notify()` 对外部 watcher：向每个 `Queue[interface{}]` 投递，**Enqueue 失败（队列已关闭）的队列被自动移除**；事件类通知包装为 `MsgOnEventMsg`，watch 类为 `MsgOnWatchMsg`。

## Queue[T] 与 RingBuffer[T]

- [queue.go](../queue.go)：`sync.Mutex` + `sync.Cond` 阻塞队列。`Enqueue`/`EnqueueBatch` 返回 bool（closed 后 false）；`Dequeue`/`DequeueAll` 阻塞至有数据或关闭；`TryDequeue(All)` 非阻塞；`Close` 后 `DequeueAll` 返回 `ok=false` 驱动消费方退出。外部 watch/event 直接复用此队列收通知。
- [ring_buffer.go](../ring_buffer.go)：环形数组，head/tail/count；满时 ×2 扩容并搬移；`PopAll` 一次性取空并清零槽位。

## System.Start 序列（[system.go](../system.go)）

1. 定 groupCount（默认 NumCPU）并创建所有 group；
2. 注入 `EventHubActorType` 的 nil creator；
3. router 缺省为 `LocalRouter`，createActorRefExFunc 缺省为本地哈希；
4. `config` 置 nil —— `IsRunning()` 就靠 `config == nil` 判断，此后所有"仅启动前"的配置方法失效；
5. 启动各 group goroutine；TickInterval>0 时启动 ticker goroutine 定期向各 group mailbox 投 tick。

`Stop()`：停 ticker → 关闭所有 group mailbox → group 消费完退出时关闭所有 actor mailbox → actor 循环退出 → `wg.Wait()`。

## 已知注意事项

- `ActorRefImpl` 被直接类型断言使用（`toActorRef.(*ActorRefImpl)`），自定义 ActorRef 实现会 panic——分布式扩展也应复用 `ActorRefImpl`（dvactor 正是如此）。
- `GetWatcheeActorRef` 类工具依赖 ActorId 字符串编码约定（dvactor 侧，见 [dvactor/docs/proxies.md](../../dvactor/docs/proxies.md)）。
- `System.Request` 的 timeout 用 `time.After` 实现，高频调用注意定时器开销。
