package vactor

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

type System interface {
	Logger

	// RegisterActorType registers an actor type with its creator function.
	// actorType: the type identifier of the actor.
	// actorCreator: a function that creates a new instance of the actor.
	RegisterActorType(actorType ActorType, actorCreator func() Actor)

	// Start initializes and starts the actor system.
	Start()

	// Stop gracefully stops the actor system.
	Stop()

	// IsRunning checks if the actor system is currently running.
	// Returns: true if the system is running, false otherwise.
	IsRunning() bool

	// Send sends a message to the specified actor.
	// actorRef: reference to the target actor.
	// msg: the message to send.
	Send(actorRef ActorRef, msg interface{})

	// Request sends a message to the specified actor and waits for a response or timeout.
	// actorRef: reference to the target actor.
	// msg: the message to send.
	// timeout: maximum duration to wait for a response.
	// Returns: the response message and an error if timeout or failure occurs.
	Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError)

	// Watch subscribes to the notify of the specified actor.
	// actorRef: reference to the actor to watch.
	// watchType: type of notification to watch for.
	// queue: the queue to receive notifications.
	Watch(actorRef ActorRef, watchType WatchType, queue *Queue[interface{}])

	// Unwatch unsubscribes from the notify of the specified actor.
	// actorRef: reference to the actor to unwatch.
	// watchType: type of notification to stop watching.
	// queue: the queue to remove from notifications.
	Unwatch(actorRef ActorRef, watchType WatchType, queue *Queue[interface{}])

	// ListenEvent subscribes to a specific event.
	// eventGroup: the group of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	// queue: the queue to receive event notifications.
	ListenEvent(eventGroup EventGroup, eventId EventId, queue *Queue[interface{}])

	// UnlistenEvent unsubscribes from a specific event.
	// eventGroup: the group of the event. events in the same group are strictly orderly.
	// eventId: the identifier of the event.
	// queue: the queue to remove from event notifications.
	UnlistenEvent(eventGroup EventGroup, eventId EventId, queue *Queue[interface{}])

	// FireEvent triggers an event to all listeners.
	// eventGroup: the group of the event.
	// eventId: the identifier of the event. events in the same group are strictly orderly.
	// message: the event message to send.
	FireEvent(eventGroup EventGroup, eventId EventId, message interface{})

	// BatchSend sends messages to multiple actors in batch.
	// actorRefs: list of actor references to send messages to.
	// messages: list of messages to send.
	// Returns: error if sending fails.
	BatchSend(actorRefs []ActorRef, messages []interface{}) VAError

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

	// SetCreateActorRefExFunc sets a custom function for creating actor references. this function will be used for distributed framework.
	// createActorRefExFunc: the custom function to use for creating actor references.
	SetCreateActorRefExFunc(createActorRefExFunc CreateActorRefExFunc)

	// SetRouter sets the message routing function for the system. this function will be used for distributed framework.
	// router: the routing function to use.
	SetRouter(router Router)

	// LocalRouter is the default local message router. this function will be used for distributed framework.
	// envelope: the message envelope to route.
	// Returns: error if routing fails.
	LocalRouter(envelope Envelope) VAError
}

type SystemConfig struct {
	// SystemId: the identifier of the system.
	SystemId SystemId

	// GroupCount: the number of actor groups in the system. if zero, it will be set to the number of CPU cores.
	GroupCount uint16

	// DefaultStopInterval: the default interval before an actor is automatically stopped if idle.
	DefaultStopInterval time.Duration

	// TickInterval: the interval for tick messages to be sent to actors.
	TickInterval time.Duration

	// LogFunc: the function to use for logging messages.
	LogFunc LogFunc
}

func NewSystem(cfgFuncs ...SystemConfigFunc) System {
	config := &SystemConfig{
		DefaultStopInterval: time.Minute * 10,
		TickInterval:        time.Second,
		GroupCount:          0,
	}
	for _, f := range cfgFuncs {
		if f != nil {
			f(config)
		}
	}
	s := &system{
		envelopeTick:  &envelopeTick{},
		config:        config,
		actorCreators: make(map[ActorType]func() Actor),
		logFunc: func(logLevel LogLevel, format string, args ...interface{}) {
			msg := fmt.Sprintf(format, args...)
			fmt.Printf("[%v]%v\n", logLevel.String(), msg)
		},
	}
	s.createActorRefExFunc = s.defaultCreateActorRefEx
	return s
}

type SystemConfigFunc func(*SystemConfig)

type system struct {
	systemId             SystemId
	actorCreators        map[ActorType]func() Actor
	actorGroups          []*actorGroup
	groupCount           uint16
	wg                   sync.WaitGroup
	ticker               *time.Ticker
	defaultStopInterval  time.Duration
	tickInterval         time.Duration
	router               Router
	envelopeTick         *envelopeTick
	config               *SystemConfig
	logFunc              LogFunc
	createActorRefExFunc CreateActorRefExFunc
}

func (s *system) RegisterActorType(actorType ActorType, actorCreator func() Actor) {
	if s.IsRunning() {
		s.LogError("cannot change config after system started")
		return
	}
	if actorType < ActorTypeStart {
		s.LogError("actorType %v is invalid, must large than %v", actorType, ActorTypeStart)
		return
	}
	s.actorCreators[actorType] = actorCreator
}

func (s *system) SetRouter(router Router) {
	if s.IsRunning() {
		s.LogError("cannot change config after system started")
		return
	}
	s.router = router
}

func (s *system) Start() {
	s.groupCount = s.config.GroupCount
	if s.groupCount == 0 {
		s.groupCount = uint16(runtime.NumCPU())
		if s.groupCount == 0 {
			s.groupCount = 1
		}
	}
	s.actorGroups = make([]*actorGroup, s.groupCount)
	for i := range s.actorGroups {
		s.actorGroups[i] = newActorGroup(s)
	}
	s.systemId = s.config.SystemId
	s.actorCreators[EventHubActorType] = func() Actor { return nil }
	s.defaultStopInterval = s.config.DefaultStopInterval
	s.tickInterval = s.config.TickInterval
	if s.router == nil {
		s.router = s.LocalRouter
	}
	if s.createActorRefExFunc == nil {
		s.createActorRefExFunc = s.defaultCreateActorRefEx
	}
	if s.config.LogFunc != nil {
		s.logFunc = s.config.LogFunc
	}
	s.config = nil
	for _, group := range s.actorGroups {
		group.start()
	}
	if s.tickInterval > 0 {
		s.ticker = time.NewTicker(s.tickInterval)
		go func() {
			for {
				_, ok := <-s.ticker.C
				if !ok {
					break
				}
				for i := range s.actorGroups {
					s.actorGroups[i].mailbox.Enqueue(s.envelopeTick)
				}
			}
		}()
	}
}

func (s *system) getActorGroup(actorRef ActorRef) *actorGroup {
	return s.actorGroups[uint16(actorRef.GetGroupSlot()-1)%s.groupCount]
}

func (s *system) BatchSend(actorRefs []ActorRef, messages []interface{}) VAError {
	if len(actorRefs) == 0 || len(messages) == 0 {
		return nil
	}
	toActorRefs := make([]ActorRef, 0, len(actorRefs))
	for _, actorRef := range actorRefs {
		if actorRef == nil {
			continue
		}
		toActorRefs = append(toActorRefs, actorRef)
	}
	if len(toActorRefs) == 0 {
		return nil
	}
	return s.sendEnvelope(&EnvelopeBatchSend{
		FromActorRef: nil,
		ToActorRefs:  toActorRefs,
		Messages:     messages,
	})
}

func (s *system) sendEnvelope(msg Envelope) VAError {
	return s.router(msg)
}

func (s *system) LocalRouter(envelope Envelope) VAError {
	switch e := envelope.(type) {
	case *EnvelopeBatchSend:
		groups := make(map[*actorGroup][]ActorRef)
		for _, toActorRef := range e.ToActorRefs {
			group := s.getActorGroup(toActorRef)
			groups[group] = append(groups[group], toActorRef)
		}
		for group, actorRefs := range groups {
			group.mailbox.Enqueue(&EnvelopeBatchSend{
				FromActorRef: e.FromActorRef,
				ToActorRefs:  actorRefs,
				Messages:     e.Messages,
			})
		}
	case *EnvelopeNotify:
		groups := make(map[*actorGroup][]ActorRef)
		for _, toActorRef := range e.ToActorRefs {
			group := s.getActorGroup(toActorRef)
			groups[group] = append(groups[group], toActorRef)
		}
		for group, actorRefs := range groups {
			group.mailbox.Enqueue(&EnvelopeNotify{
				FromActorRef: e.FromActorRef,
				ToActorRefs:  actorRefs,
				NotifyType:   e.NotifyType,
				Message:      e.Message,
			})
		}
	default:
		group := s.getActorGroup(envelope.GetToActorRef())
		group.mailbox.Enqueue(envelope)
	}
	return nil
}

func (s *system) Send(actorRef ActorRef, msg interface{}) {
	s.sendEnvelope(&EnvelopeSend{
		FromActorRef: nil,
		ToActorRef:   actorRef,
		Message:      msg,
	})
}

func (s *system) Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError) {
	c := make(chan *Response, 1)
	s.sendEnvelope(&EnvelopeOuterRequest{
		ToActorRef: actorRef,
		Message:    msg,
		RspChan:    c,
	})
	if timeout > 0 {
		select {
		case r := <-c:
			return r.Message, r.Error
		case <-time.After(timeout):
			return nil, NewTimeoutError()
		}
	} else {
		r := <-c
		return r.Message, r.Error
	}
}

func (s *system) Watch(actorRef ActorRef, watchType WatchType, queue *Queue[interface{}]) {
	if queue == nil {
		return
	}
	s.sendEnvelope(&EnvelopeOuterWatch{
		ToActorRef: actorRef,
		WatchType:  watchType,
		IsWatch:    true,
		Queue:      queue,
	})
}

func (s *system) Unwatch(actorRef ActorRef, watchType WatchType, queue *Queue[interface{}]) {
	if queue == nil {
		return
	}
	s.sendEnvelope(&EnvelopeOuterWatch{
		ToActorRef: actorRef,
		WatchType:  watchType,
		IsWatch:    false,
		Queue:      queue,
	})
}

func (s *system) ListenEvent(eventGroup EventGroup, eventId EventId, queue *Queue[interface{}]) {
	s.Watch(s.CreateActorRef(EventHubActorType, ActorId(eventGroup)), WatchType(eventId), queue)
}

func (s *system) UnlistenEvent(eventGroup EventGroup, eventId EventId, queue *Queue[interface{}]) {
	s.Unwatch(s.CreateActorRef(EventHubActorType, ActorId(eventGroup)), WatchType(eventId), queue)
}

func (s *system) FireEvent(eventGroup EventGroup, eventId EventId, message interface{}) {
	s.sendEnvelope(&EnvelopeFireNotify{
		FromActorRef: nil,
		ToActorRef:   s.CreateActorRef(EventHubActorType, ActorId(eventGroup)),
		NotifyType:   NotifyTypeEvent,
		WatchType:    WatchType(eventId),
		Message:      message,
	})
}

func (s *system) CreateActorRef(actorType ActorType, actorId ActorId) ActorRef {
	return s.CreateActorRefEx(0, actorType, actorId)
}

func (s *system) CreateActorRefEx(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef {
	return s.createActorRefExFunc(systemId, actorType, actorId)
}

func (s *system) SetCreateActorRefExFunc(createActorRefExFunc CreateActorRefExFunc) {
	if s.IsRunning() {
		s.LogError("cannot change config after system started")
		return
	}
	s.createActorRefExFunc = createActorRefExFunc
}

func (s *system) IsRunning() bool {
	return s.config == nil
}

func (s *system) defaultCreateActorRefEx(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef {
	// if actorType < ActorTypeStart {
	// 	s.LogError("actorType %v is invalid, must large than %v", actorType, ActorTypeStart)
	// 	return nil
	// }
	groupSlot := GroupSlot(0)
	hash := [2]uint8{0, 0}
	str := string(actorId)
	endIndex := len(str) - 1
	for i := range endIndex + 1 {
		hash[i%2] ^= str[endIndex-i]
	}
	groupSlot = GroupSlot((uint16(hash[1]) << 8) | uint16(hash[0]))
	if groupSlot == 0 {
		groupSlot = 1
	}
	return &ActorRefImpl{
		SystemId:  systemId,
		GroupSlot: groupSlot,
		ActorType: actorType,
		ActorId:   actorId,
	}
}

func (s *system) Stop() {
	s.ticker.Stop()
	for i := range s.actorGroups {
		s.actorGroups[i].mailbox.Close()
	}
	s.wg.Wait()
}

func (s *system) LogDebug(format string, args ...interface{}) {
	s.logFunc(DebugLevel, format, args...)
}

func (s *system) LogInfo(format string, args ...interface{}) {
	s.logFunc(InfoLevel, format, args...)
}

func (s *system) LogWarn(format string, args ...interface{}) {
	s.logFunc(WarnLevel, format, args...)
}

func (s *system) LogError(format string, args ...interface{}) {
	s.logFunc(ErrorLevel, format, args...)
}

func (s *system) LogFatal(format string, args ...interface{}) {
	s.logFunc(FatalLevel, format, args...)
}

func (s *system) LogPanic(format string, args ...interface{}) {
	s.logFunc(PanicLevel, format, args...)
	panic(fmt.Sprintf(format, args...))
}
