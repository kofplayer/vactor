package vactor_test

import (
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

// 双重 Start 被拒绝：不重建 group/ticker、不泄漏旧资源，系统继续正常工作。
func TestDoubleStartGuarded(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	})
	ts.Start() // 第二次 Start
	if !ts.LogContains("already started") {
		t.Fatal("expected duplicate start warning")
	}
	ts.Send(ts.CreateActorRef(100, "a"), "m1")
	col.WaitForMessages(t, 1, 2*time.Second, "system still working after duplicate start")
}

// 未 Start 就发消息：返回 ErrorCodeSystemNotStarted 而不是 panic。
func TestSendBeforeStartReturnsError(t *testing.T) {
	s := vactor.NewSystem() // 故意不 Start
	defer s.Stop()

	s.Send(s.CreateActorRef(100, "a"), "m") // 不应 panic
	_, err := s.Request(s.CreateActorRef(100, "a"), "m", time.Second)
	if err == nil || err.Code() != vactor.ErrorCodeSystemNotStarted {
		t.Fatalf("expected ErrorCodeSystemNotStarted, got %v", err)
	}
}

// 请求处理 panic 且未 Response：框架代为回错（ErrorCodeHandlerPanic），
// 请求方立即收到错误而不是悬挂到超时，actor 继续工作。
func TestPanicInRequestHandlerAutoResponds(t *testing.T) {
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					panic("boom")
				}
			}
		})
	})
	rsp, err := ts.Request(ts.CreateActorRef(100, "a"), "hit", 2*time.Second)
	if rsp != nil {
		t.Fatalf("rsp = %v", rsp)
	}
	if err == nil || err.Code() != vactor.ErrorCodeHandlerPanic {
		t.Fatalf("expected ErrorCodeHandlerPanic, got %v", err)
	}
	if !ts.LogContains("processMessage panic") {
		t.Fatal("expected panic log")
	}
	// actor 未被 panic 拖垮：系统仍然存活并继续处理消息
	ts.Send(ts.CreateActorRef(100, "a"), "again") // 仍会 panic，但系统存活
	time.Sleep(100 * time.Millisecond)
	if !ts.IsRunning() {
		t.Fatal("system should stay running")
	}
}

// 异步请求的用户回调 panic：被 recover，actor goroutine 存活。
func TestCallbackPanicDoesNotKillActor(t *testing.T) {
	result := make(chan bool, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.RequestAsync(ctx.CreateActorRef(101, "x"), "q", 2*time.Second,
						func(msg interface{}, err vactor.VAError) {
							result <- true
							panic("callback boom")
						})
				case string:
					result <- false // 第二条消息正常处理即证明 goroutine 存活
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Response("pong", nil)
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "a"), "go")
	// 两条事件（回调 true / 后续消息 false）的到达顺序不确定，只断言两者都发生
	sawCallback, sawFollowUp := false, false
	for i := 0; i < 2; i++ {
		if v := testutil.WaitChan(t, result, 3*time.Second, "actor events"); v {
			sawCallback = true
		} else {
			sawFollowUp = true
		}
	}
	if !sawCallback || !sawFollowUp {
		t.Fatalf("callback=%v followUp=%v", sawCallback, sawFollowUp)
	}
	testutil.WaitFor(t, 2*time.Second, "callback panic logged", func() bool {
		return ts.LogContains("callback panic")
	})
}

// ctx.LogPanic 与 System.LogPanic 语义一致（panic），且被批量 recover 捕获，
// 不拖垮 actor 与进程。
func TestContextLogPanicRecovered(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case string:
					if m == "boom" {
						ctx.LogPanic("ctx panic: %v", "boom")
						return
					}
					col.Observe(ctx)
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "a"), "boom")
	testutil.WaitFor(t, 2*time.Second, "panic logged", func() bool {
		return ts.LogContains("ctx panic: boom")
	})
	ts.Send(ts.CreateActorRef(100, "a"), "still-alive")
	col.WaitForMessages(t, 1, 2*time.Second, "actor alive after ctx.LogPanic")
}

// 向异 SystemId 的引用发消息（原 LogPanic 崩溃路径）：group 批量 recover 兜底，
// 进程与 group 均存活。
func TestForeignSystemRefDoesNotKillGroup(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	})
	badRef := &vactor.ActorRefImpl{SystemId: 9, ActorType: 100, ActorId: "x", GroupSlot: 1}
	ts.LocalRouter(&vactor.EnvelopeSend{FromActorRef: nil, ToActorRef: badRef, Message: "poison"})
	time.Sleep(100 * time.Millisecond)
	if !ts.LogContains("actor group process envelope panic") {
		t.Fatal("expected group panic log")
	}
	ts.Send(ts.CreateActorRef(100, "ok"), "fine")
	col.WaitForMessages(t, 1, 2*time.Second, "group alive after poisoned envelope")
}

// 非法 EnvelopeNotify（Event 通知缺 Message，原 nil 解引用崩溃路径）：recover 兜底。
func TestMalformedNotifyDoesNotKillActor(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	})
	ts.LocalRouter(&vactor.EnvelopeNotify{
		FromActorRef: nil,
		ToActorRefs:  []vactor.ActorRef{ts.CreateActorRef(100, "a")},
		NotifyType:   vactor.NotifyTypeEvent,
		Message:      nil,
	})
	testutil.WaitFor(t, 2*time.Second, "malformed notify logged", func() bool {
		return ts.LogContains("process batch panic")
	})
	ts.Send(ts.CreateActorRef(100, "a"), "still-alive")
	col.WaitForMessages(t, 1, 2*time.Second, "actor alive after malformed notify")
}

// VAError 错误信息带错误码，便于排障。
func TestVAErrorTextContainsCode(t *testing.T) {
	err := vactor.NewVAError(vactor.ErrorCodeTimeout)
	if err.Error() != "VaError(code=1)" {
		t.Fatalf("error text = %q", err.Error())
	}
}

// actor 间同步/异步请求的处理方 panic 且未 Response：框架代为回错。
// 覆盖 envelopeContextRequest / envelopeContextRequestAsync 的 respondErrorOnce。
func TestPanicInInnerRequestAutoResponds(t *testing.T) {
	const (
		innerCallerType vactor.ActorType = 160
		boomType        vactor.ActorType = 161
	)
	const noErr = vactor.ErrorCode(-1)
	syncGot := make(chan vactor.ErrorCode, 4)
	asyncGot := make(chan vactor.ErrorCode, 4)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(boomType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); ok {
					panic("inner boom")
				}
			}
		})
		s.RegisterActorType(innerCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				m, ok := ctx.GetMessage().(string)
				if !ok {
					return
				}
				boomRef := ctx.CreateActorRef(boomType, "b")
				switch m {
				case "sync":
					if _, err := ctx.Request(boomRef, "q", 2*time.Second); err == nil {
						syncGot <- noErr
					} else {
						syncGot <- err.Code()
					}
				case "async":
					ctx.RequestAsync(boomRef, "q", 2*time.Second,
						func(_ interface{}, err vactor.VAError) {
							if err == nil {
								asyncGot <- noErr
							} else {
								asyncGot <- err.Code()
							}
						})
				}
			}
		})
	})

	ts.Send(ts.CreateActorRef(innerCallerType, "c"), "sync")
	if got := testutil.WaitChan(t, syncGot, 3*time.Second, "inner sync panic responded"); got != vactor.ErrorCodeHandlerPanic {
		t.Fatalf("inner sync got %v, want %v", got, vactor.ErrorCodeHandlerPanic)
	}
	ts.Send(ts.CreateActorRef(innerCallerType, "c"), "async")
	if got := testutil.WaitChan(t, asyncGot, 3*time.Second, "inner async panic responded"); got != vactor.ErrorCodeHandlerPanic {
		t.Fatalf("inner async got %v, want %v", got, vactor.ErrorCodeHandlerPanic)
	}
}

// fakeActorRef 是非框架自建的 ActorRef 实现，用于验证框架的防御性行为。
type fakeActorRef struct {
	actorType vactor.ActorType
	actorId   vactor.ActorId
	systemId  vactor.SystemId
	groupSlot vactor.GroupSlot
}

func (f fakeActorRef) GetActorType() vactor.ActorType { return f.actorType }
func (f fakeActorRef) GetActorId() vactor.ActorId     { return f.actorId }
func (f fakeActorRef) GetSystemId() vactor.SystemId   { return f.systemId }
func (f fakeActorRef) GetGroupSlot() vactor.GroupSlot { return f.groupSlot }

// 目标为 nil 的信封（调用方漏填 / 畸形跨节点包）：返回错误并记日志，不得 panic。
func TestLocalRouterNilTargetDropped(t *testing.T) {
	ts := testutil.NewSystem(t, func(s vactor.System) {})
	if err := ts.LocalRouter(&vactor.EnvelopeSend{ToActorRef: nil, Message: "x"}); err == nil {
		t.Fatal("envelope with nil target should report a delivery error")
	}
	testutil.WaitFor(t, 2*time.Second, "nil target logged", func() bool {
		return ts.LogContains("nil target actor")
	})
	if !ts.IsRunning() {
		t.Fatal("system should survive a nil-target envelope")
	}
}

// 自定义 ActorRef 实现：丢弃并记日志，不得 panic（此前是类型断言 panic 路径）。
func TestUnsupportedActorRefImplementationDropped(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(210, col.Creator())
	})
	// SystemId 与本机一致（能过 systemId 校验），但不是 *ActorRefImpl
	bad := fakeActorRef{actorType: 210, actorId: "x", systemId: 0, groupSlot: 1}
	ts.LocalRouter(&vactor.EnvelopeSend{ToActorRef: bad, Message: "m"})
	testutil.WaitFor(t, 2*time.Second, "unsupported ActorRef logged", func() bool {
		return ts.LogContains("unsupported ActorRef implementation")
	})
	if col.Len() != 0 {
		t.Fatal("message to an unsupported ActorRef must not be delivered")
	}
	if !ts.IsRunning() {
		t.Fatal("system should survive an unsupported ActorRef")
	}
}

// 自定义 ActorRef 作为 watcher：忽略该订阅并记日志，不得 panic。
func TestWatchFromUnsupportedActorRefIgnored(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(211, col.Creator())
	})
	target := ts.CreateActorRef(211, "t")
	ts.Send(target, "wake")
	col.WaitForMessages(t, 1, 2*time.Second, "target activated")

	bad := fakeActorRef{actorType: 211, actorId: "bad", systemId: 0, groupSlot: 1}
	ts.LocalRouter(&vactor.EnvelopeWatch{FromActorRef: bad, ToActorRef: target, WatchType: 1, IsWatch: true})
	testutil.WaitFor(t, 2*time.Second, "unsupported watcher logged", func() bool {
		return ts.LogContains("ignore watch from unsupported ActorRef")
	})
	// 同样的自定义引用退订也不得 panic
	ts.LocalRouter(&vactor.EnvelopeWatch{FromActorRef: bad, ToActorRef: target, WatchType: 1, IsWatch: false})
	testutil.WaitFor(t, 2*time.Second, "unsupported unwatch logged", func() bool {
		return ts.LogContains("ignore unwatch from unsupported ActorRef")
	})
	if !ts.IsRunning() {
		t.Fatal("system should survive unsupported watcher refs")
	}
}
