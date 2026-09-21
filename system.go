package vactor

import (
	"context"
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

	// Stop gracefully stops the actor system and waits for every group/actor
	// goroutine to finish. It blocks indefinitely if a user handler is stuck;
	// use StopWithContext when a bounded shutdown is required.
	Stop()

	// StopWithContext stops the system like Stop but bounds the wait by ctx.
	// It returns ctx.Err() when the deadline expires first; shutdown keeps
	// running in the background and the system must not be used afterwards.
	StopWithContext(ctx context.Context) error

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
	//
	// 该上限同时作用于 **group mailbox**（组内全部流量的入口），其实际阈值为
	// MaxMailboxDepth × GroupMailboxDepthFactor——group 只做分派、信封停留时间极短，
	// 用同一绝对值会把正常突发误判成过载而误伤整组。
	//
	// 注意：超限是"丢弃"而不是"反压调用方"——Send/BatchSend 仍返回 nil（停机等
	// 其他失败场景除外），错误只体现在日志里。
	MaxMailboxDepth int

	// OuterQueueMaxDepth: 外部 watch / 事件队列（System.Watch / ListenEvent 传入的
	// *Queue[interface{}]）的深度上限。达到上限后框架丢弃新通知、记 Warn 并摘除该
	// 订阅，避免消费者停止读取时 queue 无界增长直至 OOM。
	// 0 表示不限制（默认，保持旧行为）。
	//
	// 注意：摘除是永久性的——一次溢出一个订阅就没了。请根据消费者的最坏停顿时间
	// 留出足够余量；消费者侧应保证持续 Dequeue。
	OuterQueueMaxDepth int
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
	// 用户给了 LogFunc 就立刻生效：Start 之前也会有配置类拒绝（RegisterActorType
	// 类型非法、重复 Start、Stop 后重启……），早先这些只写在 Start 里装载，结果
	// 全部绕过用户 logger 直接 printf 到 stdout，测试与生产都捕获不到。
	if config.LogFunc != nil {
		s.logFunc = config.LogFunc
	}
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
	outerQueueMaxDepth   int
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
	// Stop 之后不可重启：停机已经关掉了 mailbox/ticker，重建会让"已停止的系统"
	// 又跑起来，而 IsRunning 与调用方的认知全部反过来。要用就新建一个 System。
	if s.stopped.Load() {
		s.LogError("system already stopped and cannot be restarted; create a new System")
		return
	}
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
	s.outerQueueMaxDepth = s.config.OuterQueueMaxDepth
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

// getActorGroupIndex 计算 actor 所属 group 的下标。
// 取模用 len(actorGroups) 而不是 groupCount：两者本应相等，但用切片长度能保证
// 结果不越界（调用点都已确认 len(actorGroups) != 0）。
func (s *system) getActorGroupIndex(actorRef ActorRef) uint16 {
	return uint16(actorRef.GetGroupSlot()-1) % uint16(len(s.actorGroups))
}

func (s *system) getActorGroup(actorRef ActorRef) *actorGroup {
	return s.actorGroups[s.getActorGroupIndex(actorRef)]
}

// bucketByGroup 把收件人按所属 group 分桶，返回的切片下标即 group 下标（空桶为 nil）。
// 第二个返回值是**被跳过的 nil 引用个数**：批量投递的收件人列表可能来自远端字节流
// （dvactor 的 clusterNet.OnMessage 不经 System.BatchSend 的 nil 过滤），这里统一滤掉，
// 调用方只负责记日志。
//
// 两趟分配：先计数，再按精确容量一次性分配。若单趟 append 到 nil 切片，每个桶都会
// 经历 1,2,4,...,N 的倍增——1000 个收件人摊到 4 个 group 就是约 36 次分配；这里
// 只分配 2 个定长切片 + 非空桶各一次。顺带把投递顺序从"map 遍历"变成"按 group
// 下标"，不再依赖 map 的随机顺序。
func (s *system) bucketByGroup(refs []ActorRef) ([][]ActorRef, int) {
	n := len(s.actorGroups)
	counts := make([]int, n)
	skipped := 0
	for _, ref := range refs {
		if ref == nil {
			skipped++
			continue
		}
		counts[s.getActorGroupIndex(ref)]++
	}
	buckets := make([][]ActorRef, n)
	for i, c := range counts {
		if c > 0 {
			buckets[i] = make([]ActorRef, 0, c)
		}
	}
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		i := s.getActorGroupIndex(ref)
		buckets[i] = append(buckets[i], ref)
	}
	return buckets, skipped
}

// logSkippedNilRefs 记录被 bucketByGroup 滤掉的 nil 收件人。单独成函数是为了让
// 分桶本身保持无副作用（它在热路径上被每条批量信封调用）。
func (s *system) logSkippedNilRefs(skipped int) {
	if skipped > 0 {
		s.LogError("batch envelope has %d nil target actor ref(s), skipped", skipped)
	}
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

// GroupMailboxDepthFactor 是 group mailbox 深度上限相对于 MaxMailboxDepth 的放大倍数。
//
// 为什么放大：group mailbox 是该组全部流量的唯一入口，而信封在其中的停留时间极短
// （group 每轮 DequeueAll 一次性全量取走）。它的深度反映的是"瞬时突发 + 本组分派
// 能力"，而不是某个 actor 的消费能力。若与 actor mailbox 用同一个绝对值，突发流量
// 会先把 group 打满，导致本不该丢的消息被提前丢弃（误伤整组）。
const GroupMailboxDepthFactor = 8

// groupMailboxDepthLimit 返回 group mailbox 的深度上限，0 表示不限制。
func (s *system) groupMailboxDepthLimit() int {
	if s.maxMailboxDepth <= 0 {
		return 0
	}
	return s.maxMailboxDepth * GroupMailboxDepthFactor
}

// groupMailboxHighWater 返回 group mailbox 的高水位，0 表示不告警。
func (s *system) groupMailboxHighWater() int {
	if s.mailboxHighWaterMark <= 0 {
		return 0
	}
	return s.mailboxHighWaterMark * GroupMailboxDepthFactor
}

// dropReason 说明一次投递失败的原因。
type dropReason uint8

const (
	dropNone dropReason = iota
	dropClosed
	dropOverflow
	// dropInvalid：信封本身不可投递（缺少目标）。不属于停机也不属于过载，
	// 与 dvactor Router 对同一情形返回 ErrorCodeInvalidActor 保持一致。
	dropInvalid
)

// enqueueGroup 向 group mailbox 投递，返回 dropNone 表示成功；否则给出丢弃原因。
// group mailbox 是所有进入该组消息的入口，若无上限，突发流量或分派能力不足会让它
// 无界增长直至 OOM——MaxMailboxDepth 只保护 actor mailbox 是不够的。
func (s *system) enqueueGroup(group *actorGroup, envelope Envelope) dropReason {
	limit := s.groupMailboxDepthLimit()
	mark := s.groupMailboxHighWater()
	if limit <= 0 && mark <= 0 {
		// 两项都没配：不额外取一次 Len()（它要加锁），保持原路径开销
		if group.mailbox.Enqueue(envelope) {
			return dropNone
		}
		return dropClosed
	}
	// 丢弃与告警是两件独立的事：只配水位不配上限时也必须能告警
	depth := group.mailbox.Len()
	if limit > 0 && depth >= limit {
		s.LogError("actor group mailbox depth %d reached limit %d, message dropped", depth, limit)
		return dropOverflow
	}
	if mark > 0 && depth >= mark {
		s.LogWarn("actor group mailbox depth %d exceeds high water mark %d", depth, mark)
	}
	if group.mailbox.Enqueue(envelope) {
		return dropNone
	}
	return dropClosed
}

// LocalRouter 本地投递。mailbox 已关闭（系统停机中或已停止）时返回错误：
// 静默丢弃会让调用方误以为消息已送达。
func (s *system) LocalRouter(envelope Envelope) VAError {
	if len(s.actorGroups) == 0 {
		return NewVAError(ErrorCodeSystemNotStarted)
	}
	overflowed := false
	closed := false
	invalid := false
	report := func(r dropReason) {
		switch r {
		case dropOverflow:
			overflowed = true
		case dropClosed:
			closed = true
		case dropInvalid:
			invalid = true
		}
	}
	switch e := envelope.(type) {
	case *EnvelopeBatchSend:
		buckets, skipped := s.bucketByGroup(e.ToActorRefs)
		s.logSkippedNilRefs(skipped)
		for i, actorRefs := range buckets {
			if len(actorRefs) == 0 {
				continue
			}
			report(s.enqueueGroup(s.actorGroups[i], &EnvelopeBatchSend{
				FromActorRef: e.FromActorRef,
				ToActorRefs:  actorRefs,
				Messages:     e.Messages,
			}))
		}
	case *EnvelopeNotify:
		buckets, skipped := s.bucketByGroup(e.ToActorRefs)
		s.logSkippedNilRefs(skipped)
		for i, actorRefs := range buckets {
			if len(actorRefs) == 0 {
				continue
			}
			report(s.enqueueGroup(s.actorGroups[i], &EnvelopeNotify{
				FromActorRef: e.FromActorRef,
				ToActorRefs:  actorRefs,
				NotifyType:   e.NotifyType,
				Message:      e.Message,
			}))
		}
	default:
		toActorRef := envelope.GetToActorRef()
		if toActorRef == nil {
			// 缺少目标的信封（畸形跨节点包 / 调用方漏填）：丢弃而不是解引用 panic
			s.LogError("local router received envelope with nil target actor, dropped")
			invalid = true
			break
		}
		report(s.enqueueGroup(s.getActorGroup(toActorRef), envelope))
	}
	switch {
	case closed:
		return NewVAError(ErrorCodeSystemNotStarted)
	case overflowed:
		return NewVAError(ErrorCodeMailboxOverflow)
	case invalid:
		return NewVAError(ErrorCodeInvalidActor)
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
	// 队列由调用方创建，边界只能在这里统一施加（0 = 不限制）
	if s.outerQueueMaxDepth > 0 {
		queue.SetMaxDepth(s.outerQueueMaxDepth)
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

// IsRunning 反映"已 Start 且未 Stop"。刻意只读两个原子标志：早先它用
// `config == nil` 推断运行态，那是个被 Start 置空的普通字段，既与其它 goroutine
// 的读取构成竞争，又在"未 Start 就 Stop"时给出与事实相反的答案。
func (s *system) IsRunning() bool {
	return s.started.Load() && !s.stopped.Load()
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

// Stop 停机并无限等待收尾完成。
func (s *system) Stop() {
	_ = s.StopWithContext(context.Background())
}

// StopWithContext 停机，并把"等所有 goroutine 退出"限制在 ctx 的时限内。
//
// 为什么需要它：Stop 的语义是"关掉入口、把在途消息处理完"，而最后一个环节的
// `wg.Wait()` 没有任何上界——只要有一个用户 handler 卡住（`ctx.Request(..., 0)`
// 等一个不会来的响应、或 handler 里做阻塞 IO），Stop 就永久挂住，优雅停机变成
// 整个进程退不出去（上一轮检查里就真实发生过一次：测试包被拖到 go test 超时）。
//
// 超时返回时停机流程仍在后台继续（group mailbox 已关闭，goroutine 会陆续退出），
// 但调用方不应再使用该 System。返回的错误里带上仍存活的 goroutine 数：它是定位
// "谁还没退"的第一条线索（本方法刻意不去枚举各 group 的 actor 表——那张表只属于
// 对应 group goroutine，跨 goroutine 读会构成数据竞争）。
func (s *system) StopWithContext(ctx context.Context) error {
	if !s.stopped.CompareAndSwap(false, true) {
		return nil // 幂等：重复调用直接视为已停完
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

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.LogError("stop did not finish in time (%v), %d goroutine(s) still running; "+
			"shutdown continues in the background but this system must not be used",
			ctx.Err(), runtime.NumGoroutine())
		return ctx.Err()
	}
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
