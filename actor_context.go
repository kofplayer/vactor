package vactor

import (
	"sync/atomic"
	"time"
)

// actorInstanceSeq 为每个 actorContext 生成进程内唯一递增实例 ID，
// 用作异步请求响应的关联标识（CallbackAddress）。
// 不再使用 context 指针地址：地址复用会让旧响应错配到新实例（ABA）。
var actorInstanceSeq atomic.Uint64

type actorContextCache struct {
	watcherss      map[WatchType]map[ActorRefImpl]bool
	outerWatcherss map[WatchType]map[*Queue[interface{}]]bool
}

func newActorContext(group *actorGroup, actorRef ActorRef, mailbox *Queue[Envelope], cache *actorContextCache) *actorContext {
	actorType := actorRef.GetActorType()
	creator, ok := group.system.actorCreators[actorType]
	if !ok {
		group.system.LogError("can not find actor type %v creator", actorType)
		return nil
	}
	if cache == nil {
		cache = &actorContextCache{
			watcherss:      make(map[WatchType]map[ActorRefImpl]bool),
			outerWatcherss: make(map[WatchType]map[*Queue[interface{}]]bool),
		}
	}
	ctx := &actorContext{
		system:                    group.system,
		group:                     group,
		actorRef:                  actorRef,
		mailbox:                   mailbox,
		cache:                     cache,
		waitingAsyncCallbackInfos: make(map[CallbackId]*callbackInfo),
		onMessage:                 creator(),
		instanceId:                actorInstanceSeq.Add(1),
		syncRspChan:               make(chan *EnvelopeResponse, 1),
		stopInterval:              group.system.defaultStopInterval,
		onTickMsg:                 &MsgOnTick{},
	}
	now := time.Now()
	ctx.latestMsgTime = now
	ctx.lastActiveNano.Store(now.UnixNano())
	ctx.stopIntervalNano.Store(int64(ctx.stopInterval))
	return ctx
}

// touch 刷新"最近活跃时间"及其原子镜像（原子副本供 group 无锁粗筛）。
func (a *actorContext) touch(now time.Time) {
	a.latestMsgTime = now
	a.lastActiveNano.Store(now.UnixNano())
}

// setTickEnabled 记录本 actor 是否仍需周期性 MsgOnTick。
func (a *actorContext) setTickEnabled(enabled bool) {
	a.tickDisabled.Store(!enabled)
}

// needTick 判断本次 tick 是否需要投递给该 actor。
// 原实现每 tick 向所有存活 actor 各投一份，10 万 actor 就是每秒 10 万次入队与
// goroutine 唤醒，即便它们无事可做。关闭 tick 的 actor 仅在框架确有需要时
// （有待处理的异步回调、或闲置回收条件已满足）才继续投递。
func (a *actorContext) needTick(now time.Time) bool {
	if !a.tickDisabled.Load() {
		return true // 未声明关闭：保持旧行为
	}
	if a.pendingAsyncCallback.Load() > 0 {
		return true
	}
	if si := a.stopIntervalNano.Load(); si > 0 {
		return a.lastActiveNano.Load()+si <= now.UnixNano()
	}
	return false
}

type callbackInfo struct {
	callback func(interface{}, VAError)
	timeout  time.Duration
	outtime  time.Time
}

type actorContext struct {
	system                    *system
	group                     *actorGroup
	actorRef                  ActorRef
	mailbox                   *Queue[Envelope]
	onMessage                 func(EnvelopeContext)
	latestMsgTime             time.Time
	waitingAsyncCallbackInfos map[CallbackId]*callbackInfo
	cache                     *actorContextCache
	callbackIdBase            CallbackId
	requestIdBase             CallbackId
	instanceId                uint64
	waitingSyncRequestId      CallbackId
	syncRspChan               chan *EnvelopeResponse
	// lastSyncRspRequestId 记录已投递给 syncRspChan 的最大 requestId。
	// 仅由 group goroutine 读写（它是 syncRspChan 的唯一写入者）：用于在同代内
	// 丢弃"迟到旧响应"，避免其排空掉正在等待的新响应。context 重建后归零。
	lastSyncRspRequestId   CallbackId
	stopInterval           time.Duration
	onTickMsg              *MsgOnTick
	processingRequestCount int32
	isInvalid              bool

	// 以下字段是上面那些"仅 actor goroutine 访问"的状态的原子镜像，
	// 供 group 在投递 tick 前做无锁粗筛（group 与 actor 在不同 goroutine 上）。
	lastActiveNano       atomic.Int64
	stopIntervalNano     atomic.Int64
	pendingAsyncCallback atomic.Int32
	tickDisabled         atomic.Bool
}

// pendingRequestContext 由 Request 类消息上下文实现：
// 在用户 panic 且未调用 Response 时，框架代为回错，保证
// processingRequestCount 归零、请求方不会悬挂到超时。
type pendingRequestContext interface {
	respondErrorOnce()
}

func (a *actorContext) GetActorRef() ActorRef {
	return a.actorRef
}

func (a *actorContext) Send(actorRef ActorRef, msg interface{}) {
	_ = a.system.sendEnvelope(&EnvelopeSend{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		Message:      msg,
	})
}

func (a *actorContext) RequestAsync(actorRef ActorRef, msg interface{}, timeout time.Duration, callback func(interface{}, VAError)) {
	a.callbackIdBase++
	a.waitingAsyncCallbackInfos[a.callbackIdBase] = &callbackInfo{
		callback: callback,
		outtime:  time.Now().Add(timeout),
		timeout:  timeout,
	}
	a.pendingAsyncCallback.Add(1)
	err := a.system.sendEnvelope(&EnvelopeRequestAsync{
		FromActorRef:    a.actorRef,
		ToActorRef:      actorRef,
		Message:         msg,
		CallbackId:      a.callbackIdBase,
		CallbackAddress: a.instanceId,
	})
	if err != nil {
		// 发送失败：立即删除回调登记并同步回调错误，避免条目残留导致 actor 无法回收
		delete(a.waitingAsyncCallbackInfos, a.callbackIdBase)
		a.pendingAsyncCallback.Add(-1)
		callback(nil, err)
	}
}

func (a *actorContext) Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError) {
	if actorRef == nil {
		a.system.LogError("actor %v request with nil target actor", a.actorRef)
		return nil, NewVAError(ErrorCodeInvalidActor)
	}
	// 自请求必然死锁：EnvelopeRequest 走的是 mailbox，而本 goroutine 此刻正阻塞在
	// 等待 syncRspChan 上，永远处理不到自己发出的那条请求。直接失败，而不是让调用方
	// 干等到超时——timeout<=0 时它会永久挂死整个 actor（此后该 actor 既不能处理
	// 任何消息，也不会被闲置回收）。需要自发的请求请用 RequestAsync（异步路径不阻塞）。
	if a.isSelf(actorRef) {
		a.system.LogError("actor %v request to itself is not allowed: the request can never be processed, use RequestAsync instead", a.actorRef)
		return nil, NewVAError(ErrorCodeSelfRequest)
	}
	a.requestIdBase++
	requestId := a.requestIdBase
	a.waitingSyncRequestId = requestId
	err := a.system.sendEnvelope(&EnvelopeRequest{
		FromActorRef:    a.actorRef,
		ToActorRef:      actorRef,
		Message:         msg,
		RequestId:       requestId,
		CallbackAddress: a.instanceId,
	})

	if err != nil {
		a.waitingSyncRequestId = 0
		return nil, err
	}

	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case r := <-a.syncRspChan:
				if a.matchSyncResponse(r) {
					a.waitingSyncRequestId = 0
					return r.Message, r.Error
				}
				a.system.LogWarn("actor %v drop stale sync response, requestId %v callbackAddress %v not match waiting %v/%v", a.actorRef, r.RequestId, r.CallbackAddress, a.waitingSyncRequestId, a.instanceId)
			case <-timer.C:
				a.waitingSyncRequestId = 0
				return nil, NewVAError(ErrorCodeTimeout)
			}
		}
	}
	for {
		r := <-a.syncRspChan
		if a.matchSyncResponse(r) {
			a.waitingSyncRequestId = 0
			return r.Message, r.Error
		}
		a.system.LogWarn("actor %v drop stale sync response, requestId %v callbackAddress %v not match waiting %v/%v", a.actorRef, r.RequestId, r.CallbackAddress, a.waitingSyncRequestId, a.instanceId)
	}
}

// isSelf 判断目标引用是否指向本 actor。
// 按 SystemId+ActorType+ActorId 做逻辑相等判断，不比较 GroupSlot——同一 actor
// 的两个引用可能经不同寻址路径（CreateActorRef / CreateActorRefEx）算出不同槽位。
func (a *actorContext) isSelf(actorRef ActorRef) bool {
	return actorRef.GetSystemId() == a.actorRef.GetSystemId() &&
		actorRef.GetActorType() == a.actorRef.GetActorType() &&
		actorRef.GetActorId() == a.actorRef.GetActorId()
}

// matchSyncResponse 判断一条同步响应是否属于本 actor 实例正在等待的那次请求。
// 必须同时满足 requestId 匹配与"代一致"：CallbackAddress 非 0 时必须等于本实例
// id，否则视为上一代（已重建）context 的陈旧响应。CallbackAddress 为 0 表示对端
// 未携带（旧版本/手工构造），退化为只比对 requestId 以保持向后兼容。
func (a *actorContext) matchSyncResponse(r *EnvelopeResponse) bool {
	if r.RequestId != a.waitingSyncRequestId {
		return false
	}
	return r.CallbackAddress == 0 || r.CallbackAddress == a.instanceId
}

func (a *actorContext) Watch(actorRef ActorRef, watchType WatchType) {
	_ = a.system.sendEnvelope(&EnvelopeWatch{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		WatchType:    watchType,
		IsWatch:      true,
	})
}

func (a *actorContext) Unwatch(actorRef ActorRef, watchType WatchType) {
	_ = a.system.sendEnvelope(&EnvelopeWatch{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		WatchType:    watchType,
		IsWatch:      false,
	})
}

func (a *actorContext) addWatcher(actorRef ActorRef, watchType WatchType) {
	w, isImpl := actorRef.(*ActorRefImpl)
	if !isImpl {
		a.system.LogError("actor %v ignore watch from unsupported ActorRef %T", a.actorRef, actorRef)
		return
	}
	watchers, ok := a.cache.watcherss[watchType]
	if !ok {
		watchers = make(map[ActorRefImpl]bool)
		a.cache.watcherss[watchType] = watchers
	}
	watchers[*w] = true
}

func (a *actorContext) removeWatcher(actorRef ActorRef, watchType WatchType) {
	actorRefImpl, isImpl := actorRef.(*ActorRefImpl)
	if !isImpl {
		a.system.LogError("actor %v ignore unwatch from unsupported ActorRef %T", a.actorRef, actorRef)
		return
	}
	watchers, ok := a.cache.watcherss[watchType]
	if ok {
		if _, ok := watchers[*actorRefImpl]; ok {
			delete(watchers, *actorRefImpl)
			if len(watchers) == 0 {
				delete(a.cache.watcherss, watchType)
			}
		}
	}
}

func (a *actorContext) addOuterWatcher(queue *Queue[interface{}], watchType WatchType) {
	watchers, ok := a.cache.outerWatcherss[watchType]
	if !ok {
		watchers = make(map[*Queue[interface{}]]bool)
		a.cache.outerWatcherss[watchType] = watchers
	}
	watchers[queue] = true
}

func (a *actorContext) removeOuterWatcher(queue *Queue[interface{}], watchType WatchType) {
	watchers, ok := a.cache.outerWatcherss[watchType]
	if ok {
		if _, ok := watchers[queue]; ok {
			delete(watchers, queue)
			if len(watchers) == 0 {
				delete(a.cache.outerWatcherss, watchType)
			}
		}
	}
}

func (a *actorContext) notify(watchType WatchType, message interface{}, notifyType NotifyType) {
	watchers, ok := a.cache.watcherss[watchType]
	if ok {
		toActorRefs := make([]ActorRef, len(watchers))
		n := 0
		for key := range watchers {
			toActorRefs[n] = &key
			n++
		}
		_ = a.system.sendEnvelope(&EnvelopeNotify{
			FromActorRef: a.actorRef,
			ToActorRefs:  toActorRefs,
			NotifyType:   notifyType,
			Message: &MsgOnWatchMsg{
				ActorRef:  a.actorRef,
				WatchType: watchType,
				Message:   message,
			},
		})
	}
	outerWatchers, ok := a.cache.outerWatcherss[watchType]
	if ok {
		var msg interface{}
		switch notifyType {
		case NotifyTypeWatch:
			msg = &MsgOnWatchMsg{
				ActorRef:  a.actorRef,
				WatchType: watchType,
				Message:   message,
			}
		case NotifyTypeEvent:
			msg = &MsgOnEventMsg{
				EventGroup: EventGroup(a.actorRef.GetActorId()),
				EventId:    EventId(watchType),
				Message:    message,
			}
		}
		var queues []*Queue[interface{}]
		for queue := range outerWatchers {
			if !queue.Enqueue(msg) {
				queues = append(queues, queue)
			}
		}
		for _, queue := range queues {
			delete(outerWatchers, queue)
		}
		if len(outerWatchers) == 0 {
			delete(a.cache.outerWatcherss, watchType)
		}
	}
}

func (a *actorContext) Notify(watchType WatchType, message interface{}) {
	a.notify(watchType, message, NotifyTypeWatch)
}

func (a *actorContext) ListenEvent(eventGroup EventGroup, eventId EventId) {
	a.Watch(a.CreateActorRef(EventHubActorType, ActorId(eventGroup)), WatchType(eventId))
}

func (a *actorContext) UnlistenEvent(eventGroup EventGroup, eventId EventId) {
	a.Unwatch(a.CreateActorRef(EventHubActorType, ActorId(eventGroup)), WatchType(eventId))
}

func (a *actorContext) FireEvent(eventGroup EventGroup, eventId EventId, message interface{}) {
	_ = a.system.sendEnvelope(&EnvelopeFireNotify{
		FromActorRef: a.actorRef,
		ToActorRef:   a.CreateActorRef(EventHubActorType, ActorId(eventGroup)),
		NotifyType:   NotifyTypeEvent,
		WatchType:    WatchType(eventId),
		Message:      message,
	})
}

func (a *actorContext) BatchSend(actorRefs []ActorRef, messages []interface{}) VAError {
	return a.system.BatchSend(actorRefs, messages)
}

func (a *actorContext) SetStopInterval(d time.Duration) {
	a.touch(time.Now())
	a.stopInterval = d
	a.stopIntervalNano.Store(int64(d))
}

func (a *actorContext) SetSelfInvalid() {
	a.isInvalid = true
	a.SetStopInterval(time.Second)
}

func (a *actorContext) CreateActorRef(actorType ActorType, actorId ActorId) ActorRef {
	return a.system.CreateActorRef(actorType, actorId)
}

func (a *actorContext) CreateActorRefEx(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef {
	return a.system.CreateActorRefEx(systemId, actorType, actorId)
}

func (a *actorContext) LocalRouter(envelope Envelope) {
	_ = a.system.LocalRouter(envelope)
}

func (a *actorContext) waitingAsyncCallback() bool {
	return len(a.waitingAsyncCallbackInfos) > 0
}

func (a *actorContext) getNeedSaveCache() *actorContextCache {
	if len(a.cache.watcherss) == 0 && len(a.cache.outerWatcherss) == 0 {
		return nil
	}
	return a.cache
}

func (a *actorContext) processMessage(ctx EnvelopeContext) {
	if ctx == nil {
		return
	}
	if a.onMessage == nil || a.isInvalid {
		switch ec := ctx.(type) {
		case *envelopeContextRequestAsync:
			ec.Response(nil, NewVAError(ErrorCodeInvalidActor))
		case *envelopeContextRequest:
			ec.Response(nil, NewVAError(ErrorCodeInvalidActor))
		case *envelopeContextOuterRequest:
			ec.Response(nil, NewVAError(ErrorCodeInvalidActor))
		}
		return
	}
	defer func() {
		if r := recover(); r != nil {
			a.system.LogError("actor %v processMessage panic: %v", a.actorRef, r)
			// 用户 panic 且未 Response：代为回错，避免请求方悬挂、actor 泄漏
			if pr, ok := ctx.(pendingRequestContext); ok {
				pr.respondErrorOnce()
			}
		}
	}()
	a.onMessage(ctx)
}

func (a *actorContext) start() {
	a.system.wg.Add(1)

	go func() {
		defer func() {
			a.processMessage(&envelopeContextBase{
				actorContext: a,
				message:      &MsgOnStop{},
			})
			a.group.mailbox.Enqueue(&envelopeStopedReport{
				fromActorRef: a.actorRef,
			})
			a.system.wg.Done()
		}()
		a.touch(time.Now())
		a.processMessage(&envelopeContextBase{
			actorContext: a,
			message:      &MsgOnStart{},
		})
		for {
			msgs, ok := a.mailbox.DequeueAll()
			if !ok {
				return
			}
			if a.processBatch(msgs) {
				return
			}
		}
	}()
}

// processBatch 处理一批信封；返回 true 表示 actor 应退出（闲置回收）。
// 整批 recover：单个异常信封最多损失本批剩余消息，不会拖垮 actor goroutine。
func (a *actorContext) processBatch(msgs []Envelope) (stop bool) {
	defer func() {
		if r := recover(); r != nil {
			a.system.LogError("actor %v process batch panic: %v", a.actorRef, r)
		}
	}()
	latestIndex := len(msgs) - 1
	for n, msg := range msgs {
		msgs[n] = nil
		isTick := false
		switch t := msg.(type) {
		case *EnvelopeWatch:
			if t.IsWatch {
				a.addWatcher(t.FromActorRef, t.WatchType)
			} else {
				a.removeWatcher(t.FromActorRef, t.WatchType)
			}
			continue
		case *EnvelopeOuterWatch:
			if t.IsWatch {
				a.addOuterWatcher(t.Queue, t.WatchType)
			} else {
				a.removeOuterWatcher(t.Queue, t.WatchType)
			}
			continue
		}
		var c EnvelopeContext
		switch t := msg.(type) {
		case *EnvelopeSend:
			c = &envelopeContextSend{
				envelopeContextBase: &envelopeContextBase{
					actorContext: a,
					message:      t.Message,
					fromActorRef: t.FromActorRef,
				},
			}
		case *EnvelopeRequestAsync:
			c = &envelopeContextRequestAsync{
				envelopeContextBase: &envelopeContextBase{
					actorContext: a,
					message:      t.Message,
					fromActorRef: t.FromActorRef,
				},
				callbackId:      t.CallbackId,
				callbackAddress: t.CallbackAddress,
			}
			a.processingRequestCount++
		case *EnvelopeResponseAsync:
			a.touch(time.Now())
			if callbackInfo, ok := a.waitingAsyncCallbackInfos[t.CallbackId]; ok {
				if a.instanceId == t.CallbackAddress {
					a.invokeCallbackSafely(callbackInfo.callback, t.Message, t.Error)
					delete(a.waitingAsyncCallbackInfos, t.CallbackId)
					a.pendingAsyncCallback.Add(-1)
				} else {
					a.system.LogWarn("%v receive rsp with unknown callbackAddress: %v", a.actorRef, t.CallbackAddress)
				}
			} else {
				a.system.LogWarn("%v receive rsp with unknown callbackId: %v", a.actorRef, t.CallbackId)
			}
			continue
		case *EnvelopeRequest:
			c = &envelopeContextRequest{
				envelopeContextBase: &envelopeContextBase{
					actorContext: a,
					message:      t.Message,
					fromActorRef: t.FromActorRef,
				},
				requestId:       t.RequestId,
				callbackAddress: t.CallbackAddress,
			}
			a.processingRequestCount++
		case *EnvelopeOuterRequest:
			c = &envelopeContextOuterRequest{
				envelopeContextBase: &envelopeContextBase{
					actorContext: a,
					message:      t.Message,
				},
				rspChan: t.RspChan,
			}
		case *envelopeTick:
			waitingAsyncCallback := a.waitingAsyncCallback()
			if n >= latestIndex && a.processingRequestCount <= 0 && !waitingAsyncCallback && a.stopInterval > 0 && a.latestMsgTime.Add(a.stopInterval).Before(time.Now()) {
				return true
			}
			if waitingAsyncCallback {
				tm := make(map[CallbackId]bool)
				for id, info := range a.waitingAsyncCallbackInfos {
					if info.timeout > 0 && info.outtime.Before(time.Now()) {
						a.invokeCallbackSafely(info.callback, nil, NewVAError(ErrorCodeTimeout))
						tm[id] = true
					}
				}
				for id := range tm {
					delete(a.waitingAsyncCallbackInfos, id)
					a.pendingAsyncCallback.Add(-1)
				}
			}
			c = &envelopeContextBase{
				actorContext: a,
				message:      a.onTickMsg,
			}
			isTick = true
		case *EnvelopeBatchSend:
			ctx := &envelopeContextSend{
				envelopeContextBase: &envelopeContextBase{
					actorContext: a,
					fromActorRef: t.FromActorRef,
				},
			}
			for _, msg := range t.Messages {
				ctx.message = msg
				a.processMessage(ctx)
			}
			a.touch(time.Now())
			continue
		case *EnvelopeNotify:
			switch t.NotifyType {
			case NotifyTypeWatch:
				c = &envelopeContextNotify{
					envelopeContextBase: &envelopeContextBase{
						actorContext: a,
						fromActorRef: t.FromActorRef,
						message:      t.Message,
					},
				}
			case NotifyTypeEvent:
				c = &envelopeContextNotify{
					envelopeContextBase: &envelopeContextBase{
						actorContext: a,
						fromActorRef: t.FromActorRef,
						message: &MsgOnEventMsg{
							EventGroup: EventGroup(t.Message.GetActorId()),
							EventId:    EventId(t.Message.WatchType),
							Message:    t.Message.Message,
						},
					},
				}
			}
		case *EnvelopeFireNotify:
			a.notify(t.WatchType, t.Message, t.NotifyType)
			a.touch(time.Now())
			continue
		}
		if !isTick {
			a.touch(time.Now())
		}
		a.processMessage(c)
	}
	return false
}

// invokeCallbackSafely 执行用户异步回调；回调 panic 只记日志，不拖垮 actor goroutine。
func (a *actorContext) invokeCallbackSafely(callback func(interface{}, VAError), msg interface{}, err VAError) {
	defer func() {
		if r := recover(); r != nil {
			a.system.LogError("actor %v request callback panic: %v", a.actorRef, r)
		}
	}()
	callback(msg, err)
}
