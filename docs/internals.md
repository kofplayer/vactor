# vactor 内部机制（L2）

> 返回 [vactor/CLAUDE.md](../CLAUDE.md)。宏观流程见 [architecture.md](architecture.md)。

## Envelope 家族（[envelope.go](../envelope.go)）

所有内部流转的消息都封装为 `Envelope`（仅要求 `GetToActorRef()`）：

| Envelope | 用途 | 生产者 |
|----------|------|--------|
| `EnvelopeSend` | actor 间单向消息 | ctx.Send / system.Send |
| `EnvelopeBatchSend` | 多收件人批量（`ToActorRefs`+`Messages`） | BatchSend |
| `EnvelopeRequest` / `EnvelopeResponse` | actor 间同步请求/响应，带 `RequestId`+`CallbackAddress` | ctx.Request / ctx.Response |
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
- `EnvelopeResponseAsync` 也不进 `onMessage`：按 `CallbackId` 找到回调直接执行，并用 `CallbackAddress`（actorContext 的进程内唯一递增实例 ID）校验——防止 actor 回收重建后旧响应打到新实例（不再使用指针地址，避免地址复用导致 ABA 错配）。回调执行带 recover，用户回调 panic 只记日志。
- `processingRequestCount` 统计未完成的入向 Request（Request/RequestAsync），未归零前 actor 不会因闲置回收（避免同步响应丢失）。若处理请求 panic 且用户未 Response，框架代为回 `ErrorCodeHandlerPanic` 保证计数归零、请求方不悬挂。
- 信封分批处理（`processBatch`），整批 recover（防护语义见 [architecture.md](architecture.md)）：单个异常信封最多损失本批剩余消息并记日志，goroutine 继续运行。group 侧（[group.go](../group.go) `processBatch`）同理。
- 同步 `Request` 的响应匹配分两层：**group 侧**先按 `CallbackAddress`（本代 `instanceId`）剔除上一代的陈旧响应，再按 `requestId` **单调去旧**（记录已投递的最大值，`<=` 即丢弃迟到旧响应；仅对严格更新的响应排空并投递——不能假设"越晚到达越新"，跨目标乱序时旧响应可能后到），然后把 `EnvelopeResponse` 塞进 `syncRspChan`；**actor 侧**比对 `waitingSyncRequestId` 且要求代一致（`CallbackAddress == instanceId`；`0` 视为未携带，按旧语义只比 `RequestId`）。两层都不匹配的迟到响应直接丢弃。`System.Request`/`ctx.Request` 的超时使用可 Stop 的 `time.Timer`（非 `time.After`），发送失败（含未 Start）立即返回错误而不等待。

## Watcher 缓存与 actor 复活

`actorContextCache` 保存两张表：`watcherss`（actor watcher）与 `outerWatcherss`（外部队列 watcher），均按 `WatchType` 分组。

- actor 回收时，若两表非空则 cache 存入 `actorGroup.actorCaches`（[group.go](../group.go) `onActorStoped`）。
- 同 actor 再次激活时取回 cache → **watcher 关系在 actor 回收后仍然保留**。
- `notify()` 对外部 watcher：向每个 `Queue[interface{}]` 投递，**Enqueue 失败（队列已关闭）的队列被自动移除**；事件类通知包装为 `MsgOnEventMsg`，watch 类为 `MsgOnWatchMsg`。

## Queue[T] 与 RingBuffer[T]

- [queue.go](../queue.go)：`sync.Mutex` + `sync.Cond` 阻塞队列。`Enqueue`/`EnqueueBatch` 返回 bool（closed 后 false）；`Dequeue`/`DequeueAll` 阻塞至有数据或关闭；`TryDequeue(All)` 非阻塞；`Close` 后 `DequeueAll` 返回 `ok=false` 驱动消费方退出。外部 watch/event 直接复用此队列收通知。
- [ring_buffer.go](../ring_buffer.go)：环形数组，head/tail/count；满时 ×2 扩容并搬移；`PopAll` 一次性取空并清零槽位。

## System.Start 序列（[system.go](../system.go)）

1. 定 groupCount（默认值见 [API 参考](api-reference.md)）并创建所有 group；
2. 注入 `EventHubActorType` 的 nil creator；
3. router 缺省为 `LocalRouter`，createActorRefExFunc 缺省为本地哈希；
4. `config` 置 nil、`started` 置位 —— `IsRunning()` = `config == nil && !stopped`；`started` 守卫使所有"仅启动前"的配置方法在启动后（含 Stop 后）持续失效，双重 `Start` 被拒绝并记日志；
5. 启动各 group goroutine；TickInterval>0 时启动 ticker goroutine 定期向各 group mailbox 投 tick。

`Stop()`：停 ticker → 关闭所有 group mailbox → group 消费完退出时关闭所有 actor mailbox → actor 循环退出 → `wg.Wait()`。

## 已知注意事项

- `ActorRefImpl` 被直接类型断言使用（`toActorRef.(*ActorRefImpl)`），自定义 ActorRef 实现会 panic——分布式扩展也应复用 `ActorRefImpl`（dvactor 正是如此）。
- `GetWatcheeActorRef` 类工具依赖 ActorId 字符串编码约定（dvactor 侧，见 [dvactor/docs/proxies.md](../../dvactor/docs/proxies.md)）。
