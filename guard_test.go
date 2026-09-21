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

// 向异 SystemId 的引用发消息：只丢这一条并记日志，进程与 group 存活。
// 原实现是 LogPanic + 整批 recover——故障面是"本组当前一整批消息"，而分布式层
// （dvactor）的目标引用来自远端字节流，一条畸形包就能毒死一整批无关消息。
func TestForeignSystemRefDoesNotKillGroup(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	}, testutil.WithGroupCount(1))
	badRef := &vactor.ActorRefImpl{SystemId: 9, ActorType: 100, ActorId: "x", GroupSlot: 1}
	_ = ts.LocalRouter(&vactor.EnvelopeSend{FromActorRef: nil, ToActorRef: badRef, Message: "poison"})
	testutil.WaitFor(t, 2*time.Second, "poisoned envelope logged", func() bool {
		return ts.LogContains("envelope dropped")
	})
	if ts.LogContains("process envelope panic") {
		t.Fatal("foreign SystemId must not escalate to a batch-level panic")
	}
	ts.Send(ts.CreateActorRef(100, "ok"), "fine")
	col.WaitForMessages(t, 1, 2*time.Second, "group alive after poisoned envelope")
}

// 投毒信封排在批量投递最前时，同批其余收件人一条都不能少。
// 这是 group 整批 recover 的放大路径：不逐条隔离就会 4 次投递全丢。
func TestForeignSystemRefInBatchDoesNotCostOthers(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	}, testutil.WithGroupCount(1))
	bad := &vactor.ActorRefImpl{SystemId: 999, ActorType: 100, ActorId: "bad", GroupSlot: 1}
	ok1 := ts.CreateActorRef(100, "g1")
	ok2 := ts.CreateActorRef(100, "g2")
	// 单 group：三个引用落进同一个桶，由同一个批处理循环投递
	if err := ts.BatchSend([]vactor.ActorRef{bad, ok1, ok2}, []interface{}{"m1", "m2"}); err != nil {
		t.Fatalf("BatchSend: %v", err)
	}
	msgs := col.WaitForMessages(t, 4, 2*time.Second, "both good recipients get both messages")
	for _, m := range msgs {
		if s, _ := m.(string); s != "m1" && s != "m2" {
			t.Fatalf("unexpected message %v", m)
		}
	}
}

// 批内含 nil 收件人（远端解码出的空 ActorRef）：滤掉并记日志，其余照常投递，
// 且不得在调用方 goroutine panic——该路径可能来自没有 recover 的网络读 goroutine。
func TestBatchSendWithNilRefIsFiltered(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	}, testutil.WithGroupCount(1))
	a := ts.CreateActorRef(100, "a")
	b := ts.CreateActorRef(100, "b")
	if err := ts.BatchSend([]vactor.ActorRef{nil, a, nil, b}, []interface{}{"m1"}); err != nil {
		t.Fatalf("BatchSend: %v", err)
	}
	col.WaitForMessages(t, 2, 2*time.Second, "both valid recipients still get the message")

	// 绕过公开 API 直达路由层（dvactor 的 clusterNet.OnMessage 就是这么调的）
	_ = ts.LocalRouter(&vactor.EnvelopeBatchSend{
		ToActorRefs: []vactor.ActorRef{nil},
		Messages:    []interface{}{"x"},
	})
	testutil.WaitFor(t, 2*time.Second, "nil refs logged by local router", func() bool {
		return ts.LogContains("nil target actor ref")
	})
}

// 非法 EnvelopeNotify（Event 通知缺 Message）：逐条丢弃并记日志，
// 不再依赖整批 recover 兜底（那会连带丢掉本批其余消息）。
func TestMalformedNotifyDoesNotKillActor(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	}, testutil.WithGroupCount(1))
	target := ts.CreateActorRef(100, "a")
	_ = ts.LocalRouter(&vactor.EnvelopeNotify{
		FromActorRef: nil,
		ToActorRefs:  []vactor.ActorRef{target},
		NotifyType:   vactor.NotifyTypeEvent,
		Message:      nil,
	})
	// 同一批里紧跟一条正常消息：畸形信封不能让它一起消失
	ts.Send(target, "still-alive")
	testutil.WaitFor(t, 2*time.Second, "malformed notify logged", func() bool {
		return ts.LogContains("notify without payload")
	})
	if ts.LogContains("process batch panic") {
		t.Fatal("malformed notify must be handled per-envelope, not by batch recover")
	}
	col.WaitForMessages(t, 1, 2*time.Second, "actor alive and still receiving after malformed notify")
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
	_ = ts.LocalRouter(&vactor.EnvelopeSend{ToActorRef: bad, Message: "m"})
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
	_ = ts.LocalRouter(&vactor.EnvelopeWatch{FromActorRef: bad, ToActorRef: target, WatchType: 1, IsWatch: true})
	testutil.WaitFor(t, 2*time.Second, "unsupported watcher logged", func() bool {
		return ts.LogContains("ignore watch from unsupported ActorRef")
	})
	// 同样的自定义引用退订也不得 panic
	_ = ts.LocalRouter(&vactor.EnvelopeWatch{FromActorRef: bad, ToActorRef: target, WatchType: 1, IsWatch: false})
	testutil.WaitFor(t, 2*time.Second, "unsupported unwatch logged", func() bool {
		return ts.LogContains("ignore unwatch from unsupported ActorRef")
	})
	if !ts.IsRunning() {
		t.Fatal("system should survive unsupported watcher refs")
	}
}

// Stop 之后不可重启：停机关掉了 mailbox 与 ticker，重启会得到一个"IsRunning 说没在
// 跑、实际却在收发消息"的系统（本轮检查实测到过），因此 Start 必须拒绝。
func TestStopBeforeStartCannotRestart(t *testing.T) {
	logs := testutil.NewLogBuffer()
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.GroupCount = 1
		sc.LogFunc = logs.LogFunc
	})
	s.Stop() // 未 Start 就先停
	s.Start()
	defer s.Stop()

	if s.IsRunning() {
		t.Fatal("IsRunning must stay false: Start was rejected after Stop")
	}
	if !logs.Contains("cannot be restarted") {
		t.Fatal("the rejected restart must be logged")
	}
	// 而且系统确实不可用：发消息按"未启动"报错，而不是静默接受
	s.Send(s.CreateActorRef(100, "a"), "x")
	if _, err := s.Request(s.CreateActorRef(100, "a"), "x", 200*time.Millisecond); err == nil {
		t.Fatal("sending to a stopped system must fail")
	}
}

// 正常路径：Start 之后 IsRunning 为真，Stop 之后为假。
func TestIsRunningTracksStartAndStop(t *testing.T) {
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.GroupCount = 1
		sc.LogFunc = func(vactor.LogLevel, string, ...interface{}) {}
	})
	if s.IsRunning() {
		t.Fatal("not started yet, must report false")
	}
	s.Start()
	if !s.IsRunning() {
		t.Fatal("IsRunning must be true after Start")
	}
	s.Stop()
	if s.IsRunning() {
		t.Fatal("IsRunning must be false after Stop")
	}
}

// System.LogPanic 仍是公开 API（记日志后 panic）；调用方自行决定是否兜底。
func TestSystemLogPanicPanicsAfterLogging(t *testing.T) {
	logs := testutil.NewLogBuffer()
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) { sc.LogFunc = logs.LogFunc })
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("LogPanic must panic")
		}
		if !logs.Contains("boom") {
			t.Fatal("LogPanic must log before panicking")
		}
	}()
	s.LogPanic("boom %d", 1)
}

// 订阅缓存**永不淘汰**——这是框架契约，不是可以被"优化"掉的内存泄漏。
//
// group.actorCaches 存的是"谁在 watch 这个 actor"，属于权威状态：丢掉它不会让任何
// 一边观察到错误——订阅方没收到 Unwatch、仍以为订着，被观察方复活后 Notify 却找不到
// 人，表现为"通知莫名其妙断了"。所以闲置多久都必须还在（本用例故意跨几十个回收周期）。
// 事件总线的同一契约见 TestEventHubRecyclePreservesListeners。
func TestWatchSubscriptionSurvivesLongIdle(t *testing.T) {
	const stopInterval = 50 * time.Millisecond
	notify := make(chan *vactor.MsgOnWatchMsg, 8)

	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if m, ok := ctx.GetMessage().(string); ok && m == "ping" {
					ctx.Notify(7, "n")
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case string:
					ctx.Watch(ctx.CreateActorRef(100, "watched"), 7)
				case *vactor.MsgOnWatchMsg:
					notify <- m
				}
			}
		})
	}, testutil.WithGroupCount(1), testutil.WithStopInterval(stopInterval))

	target := ts.CreateActorRef(100, "watched")
	ts.Send(ts.CreateActorRef(101, "watcher"), "watch")
	time.Sleep(100 * time.Millisecond)

	ts.Send(target, "ping")
	if m := testutil.WaitChan(t, notify, 2*time.Second, "notify before recycling"); m.Message != "n" {
		t.Fatalf("first notify = %v", m.Message)
	}

	// 让被观察者远过回收窗口：几十个 stopInterval，任何"合理 TTL"都会被击穿
	time.Sleep(40 * stopInterval)

	ts.Send(target, "ping")
	if m := testutil.WaitChan(t, notify, 2*time.Second,
		"notify after the watched actor has been idle for 40 stop intervals"); m.Message != "n" {
		t.Fatalf("notify after long idle = %v", m.Message)
	}
}
