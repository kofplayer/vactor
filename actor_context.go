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
	return &actorContext{
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
	stopInterval              time.Duration
	onTickMsg                 *MsgOnTick
	processingRequestCount    int32
	isInvalid                 bool
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
	a.system.sendEnvelope(&EnvelopeSend{
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
		callback(nil, err)
	}
}

func (a *actorContext) Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError) {
	a.requestIdBase++
	requestId := a.requestIdBase
	a.waitingSyncRequestId = requestId
	err := a.system.sendEnvelope(&EnvelopeRequest{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		Message:      msg,
		RequestId:    requestId,
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
				if r.RequestId == a.waitingSyncRequestId {
					a.waitingSyncRequestId = 0
					return r.Message, r.Error
				}
				a.system.LogWarn("actor %v drop stale sync response, requestId %v not match waiting %v", a.actorRef, r.RequestId, a.waitingSyncRequestId)
			case <-timer.C:
				a.waitingSyncRequestId = 0
				return nil, NewVAError(ErrorCodeTimeout)
			}
		}
	}
	for {
		r := <-a.syncRspChan
		if r.RequestId == a.waitingSyncRequestId {
			a.waitingSyncRequestId = 0
			return r.Message, r.Error
		}
		a.system.LogWarn("actor %v drop stale sync response, requestId %v not match waiting %v", a.actorRef, r.RequestId, a.waitingSyncRequestId)
	}
}

func (a *actorContext) Watch(actorRef ActorRef, watchType WatchType) {
	a.system.sendEnvelope(&EnvelopeWatch{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		WatchType:    watchType,
		IsWatch:      true,
	})
}

func (a *actorContext) Unwatch(actorRef ActorRef, watchType WatchType) {
	a.system.sendEnvelope(&EnvelopeWatch{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		WatchType:    watchType,
		IsWatch:      false,
	})
}

func (a *actorContext) addWatcher(actorRef ActorRef, watchType WatchType) {
	watchers, ok := a.cache.watcherss[watchType]
	if !ok {
		watchers = make(map[ActorRefImpl]bool)
		a.cache.watcherss[watchType] = watchers
	}
	watchers[*actorRef.(*ActorRefImpl)] = true
}

func (a *actorContext) removeWatcher(actorRef ActorRef, watchType WatchType) {
	watchers, ok := a.cache.watcherss[watchType]
	if ok {
		actorRefImpl := actorRef.(*ActorRefImpl)
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
		a.system.sendEnvelope(&EnvelopeNotify{
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
	a.system.sendEnvelope(&EnvelopeFireNotify{
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
	a.latestMsgTime = time.Now()
	a.stopInterval = d
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
	a.system.LocalRouter(envelope)
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
		a.latestMsgTime = time.Now()
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
			a.latestMsgTime = time.Now()
			if callbackInfo, ok := a.waitingAsyncCallbackInfos[t.CallbackId]; ok {
				if a.instanceId == t.CallbackAddress {
					a.invokeCallbackSafely(callbackInfo.callback, t.Response.Message, t.Response.Error)
					delete(a.waitingAsyncCallbackInfos, t.CallbackId)
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
				requestId: t.RequestId,
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
			a.latestMsgTime = time.Now()
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
							EventGroup: EventGroup(t.Message.ActorRef.GetActorId()),
							EventId:    EventId(t.Message.WatchType),
							Message:    t.Message.Message,
						},
					},
				}
			}
		case *EnvelopeFireNotify:
			a.notify(t.WatchType, t.Message, t.NotifyType)
			a.latestMsgTime = time.Now()
			continue
		}
		if !isTick {
			a.latestMsgTime = time.Now()
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
