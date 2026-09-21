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
- `processingRequestCount` 统计未完成的入向 Request（Request/RequestAsync），未归零前 actor 不会因闲置回收（这是"把 ctx 存下来稍后再 Response"能工作的原因）。若处理请求 panic 且用户未 Response，框架代为回 `ErrorCodeHandlerPanic` 保证计数归零、请求方不悬挂。
- **已知未修的代价**：handler 只是**忘了**调用 `Response`（没 panic）时，这个计数会永久留在 >0，该 actor 从此永不回收、mailbox 也一起常驻 group。目前没有回收策略——按 TTL 放行回收的做法本轮实现过又撤销，方案待定。设计时的两条已知约束：(1) 不要用"一个共享的挂起起点"判超时，那会让后到的请求被最老那条的时刻判过期，一条漏应答就压掉其它挂起请求的保护窗口；(2) 撤销的代价是"永久钉住"，新方案必须明确它对"稍后再 Response"用法的兼容性。现状由 `TestUnansweredRequestPinsActorKnownLimitation` 记录（含对照组），策略落地时改写该用例。
- 信封分批处理（`processBatch`），整批 recover（防护语义见 [architecture.md](architecture.md)）：单个异常信封最多损失本批剩余消息并记日志，goroutine 继续运行。group 侧（[group.go](../group.go) `processBatch`）同理。
- 同步 `Request` 的响应匹配分两层：**group 侧**先按 `CallbackAddress`（本代 `instanceId`）剔除上一代的陈旧响应，再按 `requestId` **单调去旧**（记录已投递的最大值，`<=` 即丢弃迟到旧响应；仅对严格更新的响应排空并投递——不能假设"越晚到达越新"，跨目标乱序时旧响应可能后到），然后把 `EnvelopeResponse` 塞进 `syncRspChan`；**actor 侧**比对 `waitingSyncRequestId` 且要求代一致（`CallbackAddress == instanceId`；`0` 视为未携带，按旧语义只比 `RequestId`）。两层都不匹配的迟到响应直接丢弃。`System.Request`/`ctx.Request` 的超时使用可 Stop 的 `time.Timer`（非 `time.After`），发送失败（含未 Start）立即返回错误而不等待。

## Watcher 缓存与 actor 复活

`actorContextCache` 保存两张表：`watcherss`（actor watcher）与 `outerWatcherss`（外部队列 watcher），均按 `WatchType` 分组。

- actor 回收时，若两表非空则 cache 存入 `actorGroup.actorCaches`（[group.go](../group.go) `onActorStoped`）。
- 同 actor 再次激活时取回 cache → **watcher 关系在 actor 回收后仍然保留**。
- **这份 cache 没有任何过期/淘汰机制，也不该加**：它存的是"谁在 watch 我"这类权威状态，不是可丢弃的派生缓存。按时间或容量清掉它的后果是**双向静默**——订阅方收不到任何 Unwatch、仍以为自己订着，被观察方复活后 `notify()` 却找不到人，线上表现为"通知莫名其妙断了"，且没有任何一侧报错。内存上界因此是"历史上被订阅过的 actorId 数"，这是"actor 逻辑上永远存在"模型的既定代价。真要收紧，前提是先设计并实现一条"订阅已失效"的通知路径。回归测试：`TestWatchSubscriptionSurvivesLongIdle`（跨 40 个回收周期仍须送达）、`TestEventHubRecyclePreservesListeners`。
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
