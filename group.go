package vactor

import "time"

func newActorGroup(system *system) *actorGroup {
	return &actorGroup{
		system:         system,
		mailbox:        NewQueue[Envelope](),
		actorMailboxes: make(map[ActorRefImpl]*Queue[Envelope]),
		actorContexts:  make(map[ActorRefImpl]*actorContext),
		actorCaches:    make(map[ActorRefImpl]*actorContextCache),
	}
}

type actorGroup struct {
	system         *system
	mailbox        *Queue[Envelope]
	actorMailboxes map[ActorRefImpl]*Queue[Envelope]
	actorCaches    map[ActorRefImpl]*actorContextCache
	actorContexts  map[ActorRefImpl]*actorContext
}

func (m *actorGroup) start() {
	// group goroutine 也必须计入 system.wg：否则 Stop() 的 Wait 只等 actor
	// goroutine，可能在 group 仍在处理最后一批信封时就返回，造成 Stop 之后
	// 仍有 goroutine 访问系统内部状态（数据竞争）。
	m.system.wg.Add(1)
	go func() {
		defer m.system.wg.Done()
		for {
			envelopes, ok := m.mailbox.DequeueAll()
			if !ok {
				// mailbox 已关闭（停机中）：关闭本组全部 actor 的 mailbox 促其退出
				for _, ctx := range m.actorContexts {
					ctx.mailbox.Close()
				}
				return
			}
			m.processBatch(envelopes)
		}
	}()
}

// processBatch 处理一批 group 信封。整批 recover：异常信封（如非法引用、
// LogPanic）只损失本批剩余消息并记录日志，不会终止 group goroutine、不会击穿进程。
func (m *actorGroup) processBatch(envelopes []Envelope) {
	defer func() {
		if r := recover(); r != nil {
			m.system.LogError("actor group process envelope panic: %v", r)
		}
	}()
	for i, envelope := range envelopes {
		envelopes[i] = nil
		switch t := envelope.(type) {
		case *envelopeStopedReport:
			m.onActorStoped(t.fromActorRef)
			continue
		case *envelopeTick:
			// 只向"确实需要 tick"的 actor 投递（见 actorContext.needTick）：
			// 否则 10 万 actor 就是每秒 10 万次入队 + 唤醒，即使它们无事可做。
			now := time.Now()
			for _, ctx := range m.actorContexts {
				if ctx.needTick(now) {
					ctx.mailbox.Enqueue(envelope)
				}
			}
			continue
		case *EnvelopeBatchSend:
			toActorRefs := t.ToActorRefs
			t.ToActorRefs = nil
			for _, actorRef := range toActorRefs {
				m.processEnvelope(actorRef, envelope)
			}
			continue
		case *EnvelopeNotify:
			toActorRefs := t.ToActorRefs
			t.ToActorRefs = nil
			for _, actorRef := range toActorRefs {
				m.processEnvelope(actorRef, envelope)
			}
			continue
		}

		m.processEnvelope(envelope.GetToActorRef(), envelope)
	}
}

func (m *actorGroup) processEnvelope(toActorRef ActorRef, envelopes Envelope) {
	if toActorRef == nil {
		m.system.LogError("actor group received envelope with nil target actor, dropped")
		return
	}
	if toActorRef.GetSystemId() != m.system.systemId {
		m.system.LogPanic("actor key system %v not fit, need %v", toActorRef.GetSystemId(), m.system.systemId)
	}
	actorRefImpl, isImpl := toActorRef.(*ActorRefImpl)
	if !isImpl {
		// 仅支持框架自建的 ActorRef；外部自定义实现无法作为 map key 与序列化载体
		m.system.LogError("actor group received unsupported ActorRef implementation %T, dropped", toActorRef)
		return
	}

	// 同步响应只投递给"正在等待"的 actor：目标 actor 若已回收，说明请求方早已超时
	// 或已放弃，响应无处可投。此时不能为了投递它而重新激活一个 context（会留下
	// 无人消费的僵尸 actor），也不能塞进 mailbox。
	if resp, isResponse := envelopes.(*EnvelopeResponse); isResponse {
		actorCtx, exists := m.actorContexts[*actorRefImpl]
		if !exists {
			m.system.LogDebug("actor %v is not active, drop sync response requestId %v", toActorRef, resp.RequestId)
			return
		}
		if resp.Response == nil {
			m.system.LogError("actor %v receive sync response with nil payload, dropped", toActorRef)
			return
		}
		// 代校验：CallbackAddress 非 0 时必须等于当前 context 实例 id，否则是
		// 上一代（已回收重建）实例的陈旧响应。必须在排空/入队之前丢弃：既不让它
		// 占住容量为 1 的通道，也不让它参与后续 requestId 比较而污染新实例基准。
		// CallbackAddress 为 0 表示对端未携带（旧版本），保持向后兼容不再校验。
		if resp.CallbackAddress != 0 && resp.CallbackAddress != actorCtx.instanceId {
			m.system.LogDebug("actor %v drop sync response from stale instance %v, requestId %v", toActorRef, resp.CallbackAddress, resp.RequestId)
			return
		}
		// 同代内按 requestId 单调去旧：requestId 由同一实例的 requestIdBase 单调递增，
		// 因此"更小的 requestId = 更旧的请求"。若已投递过 >= 本条 requestId 的响应，
		// 本条必为迟到旧响应，直接丢弃——放行它会排空掉通道里正在等待的新响应。
		// 注意：不能假设"越晚到达越新"，跨目标乱序时迟到的旧响应可能后到。
		if resp.RequestId <= actorCtx.lastSyncRspRequestId {
			m.system.LogDebug("actor %v drop stale sync response, requestId %v <= last delivered %v", toActorRef, resp.RequestId, actorCtx.lastSyncRspRequestId)
			return
		}
		// 本条严格更新（requestId 更大）：通道里可能残留的响应必然更旧（否则上面的
		// 判定会先命中），排空它们再投递是安全的——容量 1 的通道不会因此丢掉正在
		// 等待的新响应。
		for drained := false; !drained; {
			select {
			case stale := <-actorCtx.syncRspChan:
				m.system.LogWarn("actor %v drop stale sync response, requestId %v", toActorRef, stale.RequestId)
			default:
				drained = true
			}
		}
		select {
		case actorCtx.syncRspChan <- resp:
			actorCtx.lastSyncRspRequestId = resp.RequestId
		default:
			m.system.LogError("actor %v sync response channel is full, message dropped", toActorRef)
		}
		return
	}

	actorMailbox, ok := m.actorMailboxes[*actorRefImpl]
	if !ok {
		actorMailbox = NewQueue[Envelope]()
		m.actorMailboxes[*actorRefImpl] = actorMailbox
	}
	// 背压：慢消费者会让 mailbox 无界增长。达到上限直接丢弃并告警，
	// 而不是把内存吃满（0 表示不限制，保持旧行为）。
	if depth := actorMailbox.Len(); m.system.maxMailboxDepth > 0 && depth >= m.system.maxMailboxDepth {
		m.system.LogError("actor %v mailbox depth %d reached limit %d, message dropped",
			toActorRef, depth, m.system.maxMailboxDepth)
		return
	} else if m.system.mailboxHighWaterMark > 0 && depth >= m.system.mailboxHighWaterMark {
		m.system.LogWarn("actor %v mailbox depth %d exceeds high water mark %d",
			toActorRef, depth, m.system.mailboxHighWaterMark)
	}
	// 这里只关心 actor 是否已存在：已存在则直接投递，不存在才新建 context。
	if _, exists := m.actorContexts[*actorRefImpl]; !exists {
		cache, ok := m.actorCaches[*actorRefImpl]
		if ok {
			delete(m.actorCaches, *actorRefImpl)
		}
		actorCtx := newActorContext(m, toActorRef, actorMailbox, cache)
		if actorCtx == nil {
			delete(m.actorMailboxes, *actorRefImpl)
			return
		}
		m.actorContexts[*actorRefImpl] = actorCtx
		actorCtx.start()
	}
	actorMailbox.Enqueue(envelopes)
}

func (m *actorGroup) onActorStoped(actorRef ActorRef) {
	actorRefImpl, isImpl := actorRef.(*ActorRefImpl)
	if !isImpl {
		m.system.LogError("onActorStoped: unsupported ActorRef implementation %T, ignored", actorRef)
		return
	}
	actorCtx, ok := m.actorContexts[*actorRefImpl]
	var cache *actorContextCache
	if ok {
		cache = actorCtx.getNeedSaveCache()
		delete(m.actorContexts, *actorRefImpl)
	}
	actorMailbox, ok := m.actorMailboxes[*actorRefImpl]
	if ok {
		if actorMailbox.Len() == 0 {
			actorMailbox.Close()
			delete(m.actorMailboxes, *actorRefImpl)
		} else {
			actorCtx := newActorContext(m, actorRef, actorMailbox, cache)
			if actorCtx != nil {
				m.actorContexts[*actorRefImpl] = actorCtx
				actorCtx.start()
				if cache != nil {
					cache = nil
				}
			}
		}
	}

	if cache != nil {
		m.actorCaches[*actorRefImpl] = cache
	}
}
