package vactor

import (
	"fmt"
	"time"
)

type EnvelopeContext interface {
	Logger

	// GetActorRef returns the reference to the current actor.
	// Returns: ActorRef of the current actor.
	GetActorRef() ActorRef

	// GetFromActorRef returns the reference to the sender actor.
	// Returns: ActorRef of the sender.
	GetFromActorRef() ActorRef

	// GetMessage returns the message contained in the envelope.
	// Returns: the message object.
	GetMessage() interface{}

	// Send sends a message to the specified actor.
	// actorRef: the target actor reference.
	// msg: the message to send.
	Send(actorRef ActorRef, msg interface{})

	// RequestAsync sends a message to the specified actor and handles the response asynchronously.
	// actorRef: the target actor reference.
	// msg: the message to send.
	// timeout: maximum duration to wait for a response.
	// callback: function to handle the response message and error.
	RequestAsync(actorRef ActorRef, msg interface{}, timeout time.Duration, callback func(interface{}, VAError))

	// Request sends a message to the specified actor and waits for a response or timeout.
	// actorRef: the target actor reference.
	// msg: the message to send.
	// timeout: maximum duration to wait for a response.
	// Returns: the response message and an error if timeout or failure occurs.
	Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError)

	// Response sends a response message to the sender.
	// msg: the response message.
	// err: error to return, if any.
	Response(msg interface{}, err VAError)

	// Watch subscribes to notifications of the specified actor.
	// actorRef: the actor to watch.
	// watchType: type of notification to watch for.
	Watch(actorRef ActorRef, watchType WatchType)

	// Unwatch unsubscribes from the notify of the specified actor.
	// actorRef: the actor to unwatch.
	// watchType: type of notification to stop watching.
	Unwatch(actorRef ActorRef, watchType WatchType)

	// Notify sends a notification to all watchers of the current actor.
	// watchType: type of notification.
	// msg: the notification message.
	Notify(watchType WatchType, msg interface{})

	// ListenEvent subscribes to a specific event.
	// eventGroup: the group of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	ListenEvent(eventGroup EventGroup, eventId EventId)

	// UnlistenEvent unsubscribes from a specific event.
	// eventGroup: the group of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	UnlistenEvent(eventGroup EventGroup, eventId EventId)

	// FireEvent triggers an event to all listeners.
	// eventGroup: the group of the event.
	// eventId: the identifier of the event. events in the same group are strictly orderly.
	// message: the event message to send.
	FireEvent(eventGroup EventGroup, eventId EventId, message interface{})

	// BatchSend sends messages to multiple actors in batch.
	// actorRefs: list of actor references to send messages to.
	// messages: list of messages to send.
	//
	// Semantics: EVERY actor in actorRefs receives EVERY message in messages
	// (cartesian broadcast), i.e. len(actorRefs)*len(messages) deliveries in total.
	// It is NOT a pairwise zip of the two slices. An empty input slice yields no
	// delivery at all.
	//
	// Returns: error if sending fails.
	BatchSend(actorRefs []ActorRef, messages []interface{}) VAError

	// SetStopInterval sets the interval before the actor is automatically stopped if idle. actor receive any message (exclude tick message) will reset the interval.
	// interval: the duration to set. zero is means never stop.
	SetStopInterval(interval time.Duration)

	// SetTickEnabled 声明本 actor 是否需要周期性的 MsgOnTick。
	// 默认开启（保持兼容）。关闭后框架仅在确有需要时（存在待处理的异步回调、
	// 或闲置回收条件已满足）才继续投递 tick——纯空闲的 actor 不再被每秒唤醒。
	// enabled: false 表示不再需要周期 tick。
	SetTickEnabled(enabled bool)

	// SetSelfInvalid marks the actor as invalid, and has following effects:
	// - Actor will not receive any further messages.
	// - All future request messages will be responded with an error.
	// - Actor will be stopped after the stop interval.
	// - After actor stopped. it can start again, and can receive message again.
	SetSelfInvalid()

	// CreateActorRef creates a reference to an actor. which system will be calculated automatically.
	// actorType: the type identifier of the actor.
	// actorId: the unique identifier of the actor.
	// Returns: the created actor reference.
	CreateActorRef(actorType ActorType, actorId ActorId) ActorRef

	// CreateActorRefEx creates a reference to an actor in a specified system.
	// systemId: the identifier of the target system.
	// actorType: the type identifier of the actor.
	// actorId: the unique identifier of the actor.
	// Returns: the created actor reference.
	CreateActorRefEx(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef

	// LocalRouter is the default local message router. this function will be used for distributed framework.
	// envelope: the message envelope to route.
	LocalRouter(envelope Envelope)
}

type envelopeContextBase struct {
	*actorContext
	message      interface{}
	fromActorRef ActorRef
}

func (a *envelopeContextBase) LogDebug(format string, args ...interface{}) {
	a.system.logFunc(DebugLevel, format, args...)
}

func (a *envelopeContextBase) LogInfo(format string, args ...interface{}) {
	a.system.logFunc(InfoLevel, format, args...)
}

func (a *envelopeContextBase) LogWarn(format string, args ...interface{}) {
	a.system.logFunc(WarnLevel, format, args...)
}

func (a *envelopeContextBase) LogError(format string, args ...interface{}) {
	a.system.logFunc(ErrorLevel, format, args...)
}

func (a *envelopeContextBase) LogFatal(format string, args ...interface{}) {
	a.system.logFunc(FatalLevel, format, args...)
}

// LogPanic 与 System.LogPanic 语义一致：记录日志后 panic。
// panic 会被 actor/group 的批量 recover 捕获，只损失当前消息，不拖垮系统。
func (a *envelopeContextBase) LogPanic(format string, args ...interface{}) {
	a.system.logFunc(PanicLevel, format, args...)
	panic(fmt.Sprintf(format, args...))
}

// SetTickEnabled 实现 EnvelopeContext：关闭后仅在框架需要时投递 tick。
func (a *envelopeContextBase) SetTickEnabled(enabled bool) {
	a.setTickEnabled(enabled)
}

func (a *envelopeContextBase) Response(msg interface{}, err VAError) {
	a.LogError("EnvelopeContext.Response called on a non-request message, this is not allowed")
}

func (a *envelopeContextBase) GetFromActorRef() ActorRef {
	return a.fromActorRef
}

func (a *envelopeContextBase) GetMessage() interface{} {
	return a.message
}

type envelopeContextSend struct {
	*envelopeContextBase
}

type envelopeContextNotify struct {
	*envelopeContextBase
}

type envelopeContextRequestAsync struct {
	*envelopeContextBase
	callbackId      CallbackId
	callbackAddress uint64
	doSendRsp       bool
}

func (a *envelopeContextRequestAsync) Response(msg interface{}, err VAError) {
	if a.fromActorRef == nil {
		a.LogError("EnvelopeContext.Response called without a valid fromActorRef, this is not allowed")
		// 响应无处可投，但仍要归还计数：否则 actor 永远满足不了回收条件（泄漏）
		a.processingRequestCount--
		return
	}
	if a.doSendRsp {
		a.LogError("EnvelopeContext.Response called more than once, this is not allowed")
		return
	}
	a.doSendRsp = true
	_ = a.system.sendEnvelope(&EnvelopeResponseAsync{
		Response: &Response{
			Message: msg,
			Error:   err,
		},
		FromActorRef:    a.actorRef,
		ToActorRef:      a.fromActorRef,
		CallbackId:      a.callbackId,
		CallbackAddress: a.callbackAddress,
	})
	a.processingRequestCount--
}

// respondErrorOnce 实现 pendingRequestContext：用户未 Response 时补发错误响应，
// 保证 processingRequestCount 归零。
func (a *envelopeContextRequestAsync) respondErrorOnce() {
	if a.doSendRsp {
		return
	}
	a.Response(nil, NewVAError(ErrorCodeHandlerPanic))
}

type envelopeContextRequest struct {
	*envelopeContextBase
	requestId       CallbackId
	callbackAddress uint64
	doSendRsp       bool
}

func (a *envelopeContextRequest) Response(msg interface{}, err VAError) {
	if a.fromActorRef == nil {
		a.LogError("EnvelopeContext.Response called without a valid fromActorRef, this is not allowed")
		// 同上：无投递目标也必须归还计数，避免 actor 永不回收
		a.processingRequestCount--
		return
	}
	if a.doSendRsp {
		a.LogError("EnvelopeContext.Response called more than once, this is not allowed")
		return
	}
	a.doSendRsp = true
	_ = a.system.sendEnvelope(&EnvelopeResponse{
		Response: &Response{
			Message: msg,
			Error:   err,
		},
		FromActorRef:    a.actorRef,
		ToActorRef:      a.fromActorRef,
		RequestId:       a.requestId,
		CallbackAddress: a.callbackAddress,
	})
	a.processingRequestCount--
}

// respondErrorOnce 实现 pendingRequestContext。
func (a *envelopeContextRequest) respondErrorOnce() {
	if a.doSendRsp {
		return
	}
	a.Response(nil, NewVAError(ErrorCodeHandlerPanic))
}

type envelopeContextOuterRequest struct {
	*envelopeContextBase
	doSendRsp bool
	rspChan   chan *Response
}

func (a *envelopeContextOuterRequest) Response(msg interface{}, err VAError) {
	if a.doSendRsp {
		a.LogError("EnvelopeContext.Response called more than once, this is not allowed")
		return
	}
	a.doSendRsp = true
	a.rspChan <- &Response{
		Message: msg,
		Error:   err,
	}
}

// respondErrorOnce 实现 pendingRequestContext。RspChan 容量为 1 且尚未写入过，
// 非阻塞写入不会失败。
func (a *envelopeContextOuterRequest) respondErrorOnce() {
	if a.doSendRsp {
		return
	}
	a.Response(nil, NewVAError(ErrorCodeHandlerPanic))
}
