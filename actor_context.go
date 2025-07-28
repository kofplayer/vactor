package vactor

import (
	"reflect"
	"time"
)

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
		system:                     group.system,
		group:                      group,
		actorRef:                   actorRef,
		mailbox:                    mailbox,
		cache:                      cache,
		waittingAsyncCallbackInfos: make(map[CallbackId]*callbackInfo),
		onMessage:                  creator(),
		callbackIdBase:             0,
		syncRspChan:                make(chan *Response, 1),
		stopInterval:               group.system.defaultStopInterval,
		onTickMsg:                  &MsgOnTick{},
	}
}

type callbackInfo struct {
	callback func(interface{}, VAError)
	timeout  time.Duration
	outtime  time.Time
}

type actorContext struct {
	system                     *system
	group                      *actorGroup
	actorRef                   ActorRef
	mailbox                    *Queue[Envelope]
	onMessage                  func(EnvelopeContext)
	lastestMsgTime             time.Time
	waittingAsyncCallbackInfos map[CallbackId]*callbackInfo
	cache                      *actorContextCache
	callbackIdBase             CallbackId
	syncRspChan                chan *Response
	stopInterval               time.Duration
	onTickMsg                  *MsgOnTick
	processeingRequestCount    int32
	isInvalid                  bool
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

func getObjectAddr(f interface{}) uint64 {
	return uint64(reflect.ValueOf(f).Pointer())
}

func (a *actorContext) RequestAsync(actorRef ActorRef, msg interface{}, timeout time.Duration, callback func(interface{}, VAError)) {
	a.callbackIdBase++
	a.waittingAsyncCallbackInfos[a.callbackIdBase] = &callbackInfo{
		callback: callback,
		outtime:  time.Now().Add(timeout),
		timeout:  timeout,
	}
	err := a.system.sendEnvelope(&EnvelopeRequestAsync{
		FromActorRef:    a.actorRef,
		ToActorRef:      actorRef,
		Message:         msg,
		CallbackId:      a.callbackIdBase,
		CallbackAddress: getObjectAddr(a),
	})
	if err != nil {
		callback(nil, err)
	}
}

func (a *actorContext) Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError) {
	err := a.system.sendEnvelope(&EnvelopeRequest{
		FromActorRef: a.actorRef,
		ToActorRef:   actorRef,
		Message:      msg,
	})

	if err != nil {
		return nil, err
	}

	if timeout > 0 {
		select {
		case r := <-a.syncRspChan:
			return r.Message, r.Error
		case <-time.After(timeout):
			a.syncRspChan = make(chan *Response, 1)
			return nil, NewVAError(ErrorCodeTimeout)
		}
	} else {
		r := <-a.syncRspChan
		return r.Message, r.Error
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
	a.lastestMsgTime = time.Now()
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

func (a *actorContext) waittingAsyncCallback() bool {
	return len(a.waittingAsyncCallbackInfos) > 0
}

func (a *actorContext) getNeedSaveCache() *actorContextCache {
	if len(a.cache.watcherss) == 0 && len(a.cache.outerWatcherss) == 0 {
		return nil
	}
	return a.cache
}

func (a *actorContext) processMessage(ctx EnvelopeContext) {
	if a.onMessage == nil || a.isInvalid {
		switch ec := ctx.(type) {
		case *envelopeContextRequstAsync:
			ec.Response(nil, NewVAError(ErrorCodeInvalidActor))
		case *envelopeContextRequst:
			ec.Response(nil, NewVAError(ErrorCodeInvalidActor))
		case *envelopeContextOuterRequst:
			ec.Response(nil, NewVAError(ErrorCodeInvalidActor))
		}
		return
	}
	defer func() {
		if r := recover(); r != nil {
			a.system.LogError("actor %v processMessage panic: %v", a.actorRef, r)
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
			// a.system.LogDebug("actor %#v stop", a.actorRef)
			a.group.mailbox.Enqueue(&envelopeStopedReport{
				fromActorRef: a.actorRef,
			})
			a.system.wg.Done()
		}()
		a.lastestMsgTime = time.Now()
		// a.system.LogDebug("actor %#v start", a.actorRef)
		a.processMessage(&envelopeContextBase{
			actorContext: a,
			message:      &MsgOnStart{},
		})
		for {
			msgs, ok := a.mailbox.DequeueAll()
			if !ok {
				return
			}
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
					c = &envelopeContextRequstAsync{
						envelopeContextBase: &envelopeContextBase{
							actorContext: a,
							message:      t.Message,
							fromActorRef: t.FromActorRef,
						},
						callbackId:      t.CallbackId,
						callbackAddress: t.CallbackAddress,
					}
					a.processeingRequestCount++
				case *EnvelopeResponseAsync:
					a.lastestMsgTime = time.Now()
					if callbackInfo, ok := a.waittingAsyncCallbackInfos[t.CallbackId]; ok {
						if getObjectAddr(a) == t.CallbackAddress {
							callbackInfo.callback(t.Response.Message, t.Response.Error)
							delete(a.waittingAsyncCallbackInfos, t.CallbackId)
						} else {
							a.system.LogWarn("%v receive rsp with unknown callbackAddress: %v", a.actorRef, t.CallbackAddress)
						}
					} else {
						a.system.LogWarn("%v receive rsp with unknown callbackId: %v", a.actorRef, t.CallbackId)
					}
					continue
				case *EnvelopeRequest:
					c = &envelopeContextRequst{
						envelopeContextBase: &envelopeContextBase{
							actorContext: a,
							message:      t.Message,
							fromActorRef: t.FromActorRef,
						},
					}
					a.processeingRequestCount++
				case *EnvelopeOuterRequest:
					c = &envelopeContextOuterRequst{
						envelopeContextBase: &envelopeContextBase{
							actorContext: a,
							message:      t.Message,
						},
						rspChan: t.RspChan,
					}
				case *envelopeTick:
					waittingAsyncCallback := a.waittingAsyncCallback()
					if n >= latestIndex && a.processeingRequestCount <= 0 && !waittingAsyncCallback && a.stopInterval > 0 && a.lastestMsgTime.Add(a.stopInterval).Before(time.Now()) {
						return
					}
					if waittingAsyncCallback {
						tm := make(map[CallbackId]bool)
						for id, info := range a.waittingAsyncCallbackInfos {
							if info.timeout > 0 && info.outtime.Before(time.Now()) {
								info.callback(nil, NewVAError(ErrorCodeTimeout))
								tm[id] = true
							}
						}
						for id := range tm {
							delete(a.waittingAsyncCallbackInfos, id)
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
					a.lastestMsgTime = time.Now()
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
				}
				if !isTick {
					a.lastestMsgTime = time.Now()
				}
				a.processMessage(c)
			}
		}
	}()
}
