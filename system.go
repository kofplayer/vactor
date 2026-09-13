package vactor

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
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
	//
	// Semantics: EVERY actor in actorRefs receives EVERY message in messages
	// (cartesian broadcast), i.e. len(actorRefs)*len(messages) deliveries in total.
	// It is NOT a pairwise zip of the two slices. An empty input slice yields no
	// delivery at all.
	//
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

	// MailboxHighWaterMark: actor mailbox 深度达到该值即记 Warn（只告警不丢弃）。
	// 0 表示不告警。用于观测慢消费者导致的积压。
	MailboxHighWaterMark int

	// MaxMailboxDepth: actor mailbox 深度上限，达到上限的消息会被丢弃并记 Error，
	// 避免慢消费者导致 mailbox 无界增长直至 OOM。0 表示不限制（保持旧行为）。
	MaxMailboxDepth int
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
	stopChan             chan struct{}
	stopped              atomic.Bool
	started              atomic.Bool
	defaultStopInterval  time.Duration
	tickInterval         time.Duration
	mailboxHighWaterMark int
	maxMailboxDepth      int
	router               Router
	envelopeTick         *envelopeTick
	config               *SystemConfig
	logFunc              LogFunc
	createActorRefExFunc CreateActorRefExFunc
}

// startedGuard 拦截"仅允许启动前"的配置操作：启动前与启动后（含 Stop 后）均拒绝。
func (s *system) startedGuard() bool {
	if s.started.Load() {
		s.LogError("cannot change config after system started")
		return true
	}
	return false
}

func (s *system) RegisterActorType(actorType ActorType, actorCreator func() Actor) {
	if s.startedGuard() {
		return
	}
	if actorType < ActorTypeStart {
		s.LogError("actorType %v is invalid, must large than %v", actorType, ActorTypeStart)
		return
	}
	if _, exists := s.actorCreators[actorType]; exists {
		s.LogWarn("actorType %v re-registered, new creator overrides the old one", actorType)
	}
	s.actorCreators[actorType] = actorCreator
}

func (s *system) SetRouter(router Router) {
	if s.startedGuard() {
		return
	}
	s.router = router
}

func (s *system) Start() {
	if !s.started.CompareAndSwap(false, true) {
		s.LogError("system already started, ignore duplicate Start")
		return
	}
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
	s.mailboxHighWaterMark = s.config.MailboxHighWaterMark
	s.maxMailboxDepth = s.config.MaxMailboxDepth
	if s.router == nil {
		s.router = s.LocalRouter
	}
	if s.createActorRefExFunc == nil {
		s.createActorRefExFunc = s.defaultCreateActorRefEx
	}
	if s.config.LogFunc != nil {
		s.logFunc = s.config.LogFunc
	}
	// 配置自相矛盾：关掉了 tick 循环，却还设置了闲置回收时间。tick 是回收检查与
	// 异步请求超时扫描的唯一时机，此组合下 DefaultStopInterval 永远不会生效。
	if s.tickInterval <= 0 && s.defaultStopInterval > 0 {
		s.LogWarn("TickInterval <= 0 disables the tick loop: idle-actor recycling and async-request timeout scanning are both off, but DefaultStopInterval=%v is set and will never take effect", s.defaultStopInterval)
	}
	s.config = nil
	for _, group := range s.actorGroups {
		group.start()
	}
	if s.tickInterval > 0 {
		s.stopChan = make(chan struct{})
		s.ticker = time.NewTicker(s.tickInterval)
		go func() {
			for {
				select {
				case <-s.stopChan:
					return
				case _, ok := <-s.ticker.C:
					if !ok {
						return
					}
					for i := range s.actorGroups {
						s.actorGroups[i].mailbox.Enqueue(s.envelopeTick)
					}
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
	if s.router == nil {
		// Start 之前（router 未注入）：消息无法投递，直接报错而不是 panic
		return NewVAError(ErrorCodeSystemNotStarted)
	}
	return s.router(msg)
}

// LocalRouter 本地投递。mailbox 已关闭（系统停机中或已停止）时返回错误：
// 静默丢弃会让调用方误以为消息已送达。
func (s *system) LocalRouter(envelope Envelope) VAError {
	if len(s.actorGroups) == 0 {
		return NewVAError(ErrorCodeSystemNotStarted)
	}
	dropped := false
	switch e := envelope.(type) {
	case *EnvelopeBatchSend:
		groups := make(map[*actorGroup][]ActorRef)
		for _, toActorRef := range e.ToActorRefs {
			group := s.getActorGroup(toActorRef)
			groups[group] = append(groups[group], toActorRef)
		}
		for group, actorRefs := range groups {
			if !group.mailbox.Enqueue(&EnvelopeBatchSend{
				FromActorRef: e.FromActorRef,
				ToActorRefs:  actorRefs,
				Messages:     e.Messages,
			}) {
				dropped = true
			}
		}
	case *EnvelopeNotify:
		groups := make(map[*actorGroup][]ActorRef)
		for _, toActorRef := range e.ToActorRefs {
			group := s.getActorGroup(toActorRef)
			groups[group] = append(groups[group], toActorRef)
		}
		for group, actorRefs := range groups {
			if !group.mailbox.Enqueue(&EnvelopeNotify{
				FromActorRef: e.FromActorRef,
				ToActorRefs:  actorRefs,
				NotifyType:   e.NotifyType,
				Message:      e.Message,
			}) {
				dropped = true
			}
		}
	default:
		toActorRef := envelope.GetToActorRef()
		if toActorRef == nil {
			// 缺少目标的信封（畸形跨节点包 / 调用方漏填）：丢弃而不是解引用 panic
			s.LogError("local router received envelope with nil target actor, dropped")
			dropped = true
			break
		}
		group := s.getActorGroup(toActorRef)
		if !group.mailbox.Enqueue(envelope) {
			dropped = true
		}
	}
	if dropped {
		return NewVAError(ErrorCodeSystemNotStarted)
	}
	return nil
}

func (s *system) Send(actorRef ActorRef, msg interface{}) {
	_ = s.sendEnvelope(&EnvelopeSend{
		FromActorRef: nil,
		ToActorRef:   actorRef,
		Message:      msg,
	})
}

func (s *system) Request(actorRef ActorRef, msg interface{}, timeout time.Duration) (interface{}, VAError) {
	c := make(chan *Response, 1)
	err := s.sendEnvelope(&EnvelopeOuterRequest{
		ToActorRef: actorRef,
		Message:    msg,
		RspChan:    c,
		Timeout:    timeout,
	})
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case r := <-c:
			return r.Message, r.Error
		case <-timer.C:
			return nil, NewVAError(ErrorCodeTimeout)
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
	_ = s.sendEnvelope(&EnvelopeOuterWatch{
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
	_ = s.sendEnvelope(&EnvelopeOuterWatch{
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
	_ = s.sendEnvelope(&EnvelopeFireNotify{
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
	if s.startedGuard() {
		return
	}
	s.createActorRefExFunc = createActorRefExFunc
}

func (s *system) IsRunning() bool {
	return s.config == nil && !s.stopped.Load()
}

func (s *system) defaultCreateActorRefEx(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef {
	// 这里刻意不校验 actorType 下限：CreateActorRef 也用于 EventHubActorType(1)
	// 这类保留类型，业务类型的下限校验放在 RegisterActorType。
	// 取 32 位 FNV-1a 的低 16 位作分片槽位（0 归一到 1）。与 dvactor 的跨节点
	// 放置共用 HashActorId，保证同一 actor 在两层上的落点一致。
	groupSlot := GroupSlot(HashActorId(actorId) & 0xFFFF)
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
	if !s.stopped.CompareAndSwap(false, true) {
		return
	}
	if s.ticker != nil {
		s.ticker.Stop()
	}
	if s.stopChan != nil {
		close(s.stopChan)
	}
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
