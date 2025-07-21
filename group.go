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
	go func() {
		for {
			envelopes, ok := m.mailbox.DequeueAll()
			if !ok {
				for _, ctx := range m.actorContexts {
					ctx.mailbox.Close()
				}
				return
			}
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
	}()
}

func (m *actorGroup) processEnvelope(toActorRef ActorRef, envelopes Envelope) {
	if toActorRef.GetSystemId() != m.system.systemId {
		m.system.LogPanic("actor key system %v not fit, need %v", toActorRef.GetSystemId(), m.system.systemId)
	}
	actorRefImpl := toActorRef.(*ActorRefImpl)
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
		m.actorContexts[*actorRefImpl] = actorCtx
		actorCtx.start()
	}
	if t, ok := envelopes.(*EnvelopeResponse); ok {
		select {
		case actorCtx.syncRspChan <- t.Response:
		default:
			m.system.LogError("actor sync response channel is full, message dropped")
		}
	} else {
		actorMailbox.Enqueue(envelopes)
	}
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
			m.actorContexts[*actorRefImpl] = actorCtx
			actorCtx.start()
			if cache != nil {
				cache = nil
			}
		}
	}

	if cache != nil {
		m.actorCaches[*actorRefImpl] = cache
	}
}
