package vactor

import "testing"

// newSyncResponseGroup 构造一个最小 group + 手工 actorContext（不启动 goroutine），
// 用于直接驱动 processEnvelope 的同步响应投递逻辑。
func newSyncResponseGroup(t *testing.T) (*actorGroup, ActorRef, *actorContext) {
	t.Helper()
	s := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
	}).(*system)
	s.actorCreators[ActorTypeStart+1] = func() Actor {
		return func(EnvelopeContext) {}
	}
	s.systemId = 0
	g := newActorGroup(s)
	ref := &ActorRefImpl{ActorType: ActorTypeStart + 1, ActorId: "caller", GroupSlot: 1}
	ctx := newActorContext(g, ref, NewQueue[Envelope](), nil)
	if ctx == nil {
		t.Fatal("newActorContext returned nil")
	}
	g.actorContexts[*ref] = ctx
	return g, ref, ctx
}

func deliverSyncRsp(g *actorGroup, ref ActorRef, requestId CallbackId, addr uint64, msg string) {
	g.processEnvelope(ref, &EnvelopeResponse{
		Response:        &Response{Message: msg},
		ToActorRef:      ref,
		RequestId:       requestId,
		CallbackAddress: addr,
	})
}

func collectSyncRsp(ch chan *EnvelopeResponse) []*EnvelopeResponse {
	var out []*EnvelopeResponse
	for {
		select {
		case r := <-ch:
			out = append(out, r)
		default:
			return out
		}
	}
}

// 同代乱序：迟到的旧响应（更小 requestId）不得排空掉已在通道里的、正在等待的新响应。
// 这是修复前"无条件排空 + 后到者覆盖"会丢响应的场景。
func TestGroupSyncResponseKeepsAwaitedWhenLateOlderArrives(t *testing.T) {
	g, ref, ctx := newSyncResponseGroup(t)

	deliverSyncRsp(g, ref, 3, ctx.instanceId, "r3")
	deliverSyncRsp(g, ref, 2, ctx.instanceId, "r2") // 迟到旧响应

	got := collectSyncRsp(ctx.syncRspChan)
	if len(got) != 1 || got[0].Message != "r3" {
		t.Fatalf("late older response must be dropped, channel = %v", msgs(got))
	}
}

// 正常顺序：更新的响应（更大 requestId）替换通道里更旧的残留。
func TestGroupSyncResponseNewerReplacesOlder(t *testing.T) {
	g, ref, ctx := newSyncResponseGroup(t)

	deliverSyncRsp(g, ref, 1, ctx.instanceId, "r1")
	deliverSyncRsp(g, ref, 2, ctx.instanceId, "r2")

	got := collectSyncRsp(ctx.syncRspChan)
	if len(got) != 1 || got[0].Message != "r2" {
		t.Fatalf("newer response must replace older, channel = %v", msgs(got))
	}
}

// 重复 requestId 只投递一次。
func TestGroupSyncResponseDropsDuplicateRequestId(t *testing.T) {
	g, ref, ctx := newSyncResponseGroup(t)

	deliverSyncRsp(g, ref, 2, ctx.instanceId, "first")
	deliverSyncRsp(g, ref, 2, ctx.instanceId, "dup")

	got := collectSyncRsp(ctx.syncRspChan)
	if len(got) != 1 || got[0].Message != "first" {
		t.Fatalf("duplicate requestId must be dropped, channel = %v", msgs(got))
	}
}

// 代校验：CallbackAddress 非 0 且不等于本实例 id 时丢弃；0 表示未携带、按旧语义接受。
func TestGroupSyncResponseInstanceGuard(t *testing.T) {
	g, ref, ctx := newSyncResponseGroup(t)

	deliverSyncRsp(g, ref, 1, ctx.instanceId+1, "foreign")
	if got := collectSyncRsp(ctx.syncRspChan); len(got) != 0 {
		t.Fatalf("foreign instance response must be dropped, channel = %v", msgs(got))
	}

	deliverSyncRsp(g, ref, 1, 0, "legacy")
	got := collectSyncRsp(ctx.syncRspChan)
	if len(got) != 1 || got[0].Message != "legacy" {
		t.Fatalf("legacy zero address must be accepted, channel = %v", msgs(got))
	}
}

func msgs(rsps []*EnvelopeResponse) []interface{} {
	out := make([]interface{}, 0, len(rsps))
	for _, r := range rsps {
		out = append(out, r.Message)
	}
	return out
}
