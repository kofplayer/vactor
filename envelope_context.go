package vactor

import (
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
	RequestAsync(actorRef ActorRef, msg interface{}, timeout time.Duration, callback func(interface{}, error))

	// Request sends a message to the specified actor and waits for a response or timeout.
	// actorRef: the target actor reference.
	// msg: the message to send.
	// timeout: maximum duration to wait for a response.
	// Returns: the response message and an error if timeout or failure occurs.
	Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, error)

	// Response sends a response message to the sender.
	// msg: the response message.
	// err: error to return, if any.
	Response(msg interface{}, err error)

	// Watch subscribes to notifications of the specified actor.
	// actorRef: the actor to watch.
	// watchType: type of notification to watch for.
	Watch(actorRef ActorRef, watchType WatchType)

	// Unwatch unsubscribes from notifications of the specified actor.
	// actorRef: the actor to unwatch.
	// watchType: type of notification to stop watching.
	Unwatch(actorRef ActorRef, watchType WatchType)

	// Notify sends a notification to all watchers of the current actor.
	// watchType: type of notification.
	// msg: the notification message.
	Notify(watchType WatchType, msg interface{})

	// ListenEvent subscribes to a specific event.
	// eventGroup: the group/category of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	ListenEvent(eventGroup EventGroup, eventId EventId)

	// UnlistenEvent unsubscribes from a specific event.
	// eventGroup: the group/category of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	UnlistenEvent(eventGroup EventGroup, eventId EventId)

	// FireEvent triggers an event to all listeners.
	// eventGroup: the group/category of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	// message: the event message to send.
	FireEvent(eventGroup EventGroup, eventId EventId, message interface{})

	// BatchSend sends messages to multiple actors in batch.
	// actorRefs: list of actor references to send messages to.
	// messages: list of messages to send.
	// Returns: error if sending fails.
	BatchSend(actorRefs []ActorRef, messages []interface{}) error

	// SetStopInterval sets the interval before the actor is automatically stopped if idle. actor receive any message (exclude tick message) will reset the interval.
	// interval: the duration to set. zero is means never stop.
	SetStopInterval(interval time.Duration)

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
	a.actorContext.system.logFunc(DebugLevel, format, args...)
}

func (a *envelopeContextBase) LogInfo(format string, args ...interface{}) {
	a.actorContext.system.logFunc(InfoLevel, format, args...)
}

func (a *envelopeContextBase) LogWarn(format string, args ...interface{}) {
	a.actorContext.system.logFunc(WarnLevel, format, args...)
}

func (a *envelopeContextBase) LogError(format string, args ...interface{}) {
	a.actorContext.system.logFunc(ErrorLevel, format, args...)
}

func (a *envelopeContextBase) LogFatal(format string, args ...interface{}) {
	a.actorContext.system.logFunc(FatalLevel, format, args...)
}

func (a *envelopeContextBase) LogPanic(format string, args ...interface{}) {
	a.actorContext.system.logFunc(PanicLevel, format, args...)
}

func (a *envelopeContextBase) Response(msg interface{}, err error) {
	a.LogError("MsgContext.SendRsp called, this is not allowed")
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

type envelopeContextRequstAsync struct {
	*envelopeContextBase
	callbackId      CallbackId
	callbackAddress uint64
	doSendRsp       bool
}

func (a *envelopeContextRequstAsync) Response(msg interface{}, err error) {
	if a.fromActorRef == nil {
		a.LogError("MsgContext.SendRsp called without a valid fromActorRef, this is not allowed")
		return
	}
	if a.doSendRsp {
		a.LogError("MsgContext.SendRsp called more than once, this is not allowed")
		return
	}
	a.doSendRsp = true
	a.system.sendEnvelope(&EnvelopeResponseAsync{
		Response: &Response{
			Message: msg,
			Error:   err,
		},
		FromActorRef:    a.actorRef,
		ToActorRef:      a.fromActorRef,
		CallbackId:      a.callbackId,
		CallbackAddress: a.callbackAddress,
	})
	a.processeingRequestCount--
}

type envelopeContextRequst struct {
	*envelopeContextBase
	doSendRsp bool
}

func (a *envelopeContextRequst) Response(msg interface{}, err error) {
	if a.fromActorRef == nil {
		a.LogError("MsgContext.SendRsp called without a valid fromActorRef, this is not allowed")
		return
	}
	if a.doSendRsp {
		a.LogError("MsgContext.SendRsp called more than once, this is not allowed")
		return
	}
	a.doSendRsp = true
	a.system.sendEnvelope(&EnvelopeResponse{
		Response: &Response{
			Message: msg,
			Error:   err,
		},
		FromActorRef: a.actorRef,
		ToActorRef:   a.fromActorRef,
	})
	a.processeingRequestCount--
}

type envelopeContextOuterRequst struct {
	*envelopeContextBase
	doSendRsp bool
	rspChan   chan *Response
}

func (a *envelopeContextOuterRequst) Response(msg interface{}, err error) {
	if a.doSendRsp {
		a.LogError("MsgContext.SendRsp called more than once, this is not allowed")
		return
	}
	a.doSendRsp = true
	a.rspChan <- &Response{
		Message: msg,
		Error:   err,
	}
}
