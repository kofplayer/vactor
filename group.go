package vactor

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
			for _, ctx := range m.actorContexts {
				ctx.mailbox.Enqueue(envelope)
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
	if toActorRef.GetSystemId() != m.system.systemId {
		m.system.LogPanic("actor key system %v not fit, need %v", toActorRef.GetSystemId(), m.system.systemId)
	}
	actorRefImpl := toActorRef.(*ActorRefImpl)

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
		// syncRspChan 容量为 1：先排空可能残留的陈旧响应，否则本次有效响应会被
		// select-default 丢弃——那会让该 actor 之后的每一次同步请求都超时。
		// 越晚到达的响应越新，排空旧值是安全的。
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
	actorCtx, ok := m.actorContexts[*actorRefImpl]
	if !ok {
		cache, ok := m.actorCaches[*actorRefImpl]
		if ok {
			delete(m.actorCaches, *actorRefImpl)
		}
		actorCtx = newActorContext(m, toActorRef, actorMailbox, cache)
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
	actorRefImpl := actorRef.(*ActorRefImpl)
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
