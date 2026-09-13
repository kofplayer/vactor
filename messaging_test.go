package vactor_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

func TestSendDeliversExactObject(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	})
	payload := &struct{ K int }{K: 1} // 指针类型：验证投递的是同一对象
	ts.Send(ts.CreateActorRef(100, "a"), payload)
	msgs := col.WaitForMessages(t, 1, 2*time.Second, "payload delivered")
	if msgs[0] != payload {
		t.Fatal("delivered object should be the exact same reference")
	}
}

func TestSendFromActorCarriesFrom(t *testing.T) {
	fromSeen := make(chan vactor.ActorRef, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor { // sender
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Send(ctx.CreateActorRef(101, "b"), "x")
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor { // receiver
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					fromSeen <- ctx.GetFromActorRef()
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "a"), "go")
	from := testutil.WaitChan(t, fromSeen, 2*time.Second, "from actor ref")
	if from.GetActorType() != 100 || from.GetActorId() != "a" {
		t.Fatalf("from = %v/%v", from.GetActorType(), from.GetActorId())
	}
}

func TestOuterSendFromRefIsNil(t *testing.T) {
	fromSeen := make(chan vactor.ActorRef, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					fromSeen <- ctx.GetFromActorRef()
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(101, "b"), "x")
	if from := testutil.WaitChan(t, fromSeen, 2*time.Second, "from ref"); from != nil {
		t.Fatalf("outer send from should be nil, got %v", from)
	}
}

// BatchSend 语义：所有 ref 各自收到全部 messages（笛卡尔广播），且每个 actor 内保序。
func TestBatchSendCrossProduct(t *testing.T) {
	ca, cb := &testutil.Collector{}, &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, ca.Creator())
		s.RegisterActorType(101, cb.Creator())
	})
	refs := []vactor.ActorRef{
		ts.CreateActorRef(100, "a"),
		ts.CreateActorRef(101, "b"),
	}
	msgs := []interface{}{"m1", "m2"}
	if err := ts.BatchSend(refs, msgs); err != nil {
		t.Fatalf("batch send: %v", err)
	}
	for name, c := range map[string]*testutil.Collector{"a": ca, "b": cb} {
		got := c.WaitForMessages(t, 2, 2*time.Second, "batch delivered to "+name)
		if got[0] != "m1" || got[1] != "m2" {
			t.Fatalf("per-actor order broken for %s: %v", name, got)
		}
	}
}

func TestBatchSendEmptyInputNoError(t *testing.T) {
	ts := testutil.NewSystem(t, nil)
	if err := ts.BatchSend(nil, nil); err != nil {
		t.Fatalf("empty batch should be no-op, got %v", err)
	}
	if err := ts.BatchSend([]vactor.ActorRef{nil}, []interface{}{"m"}); err != nil {
		t.Fatalf("nil refs should be skipped, got %v", err)
	}
}

// 并发顺序保证：每个发送者独占一组 actor，按序号发送；actor 侧校验严格递增、无缺失无重复。
func TestConcurrentSendOrderPerActor(t *testing.T) {
	const senders, perSender = 4, 200

	var failures atomic.Int32
	var doneCount atomic.Int32
	allDone := make(chan struct{})
	state := struct {
		mu   sync.Mutex
		last map[vactor.ActorId]int64
	}{last: make(map[vactor.ActorId]int64)}

	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				m, ok := ctx.GetMessage().(int64)
				if !ok {
					return
				}
				state.mu.Lock()
				prev := state.last[ctx.GetActorRef().GetActorId()]
				state.last[ctx.GetActorRef().GetActorId()] = m
				state.mu.Unlock()
				if m != prev+1 {
					t.Errorf("actor %v sequence broken: got %d after %d",
						ctx.GetActorRef().GetActorId(), m, prev)
					failures.Add(1)
				}
				if doneCount.Add(1) == int32(senders*perSender) {
					close(allDone)
				}
			}
		})
	}, testutil.WithGroupCount(2))

	for p := 0; p < senders; p++ {
		p := p
		go func() {
			ref := ts.CreateActorRef(100, vactor.ActorId(string(rune('a'+p))))
			for i := int64(1); i <= perSender; i++ {
				ts.Send(ref, i)
			}
		}()
	}

	select {
	case <-allDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout, processed %d/%d", doneCount.Load(), senders*perSender)
	}
	if failures.Load() != 0 {
		t.Fatalf("%d sequence violations", failures.Load())
	}
}

// GroupSlot 哈希：确定性、非零、有区分度。
func TestCreateActorRefGroupSlotHash(t *testing.T) {
	ts := testutil.NewSystem(t, nil)

	r1 := ts.CreateActorRef(100, "same-id")
	r2 := ts.CreateActorRef(100, "same-id")
	if r1.GetGroupSlot() != r2.GetGroupSlot() {
		t.Fatalf("hash not deterministic: %v vs %v", r1.GetGroupSlot(), r2.GetGroupSlot())
	}
	if r1.GetGroupSlot() == 0 {
		t.Fatal("group slot must never be 0")
	}

	slots := map[vactor.GroupSlot]bool{}
	for i := 0; i < 50; i++ {
		ref := ts.CreateActorRef(100, vactor.ActorId("id-"+vactor.ActorId(rune('a'+i))))
		if ref.GetGroupSlot() == 0 {
			t.Fatal("group slot must never be 0")
		}
		slots[ref.GetGroupSlot()] = true
	}
	if len(slots) < 2 {
		t.Fatal("hash has no spread across different ids")
	}
}

// CreateActorRefEx 透传 SystemId（仅创建引用，不发送——单机系统向异 SystemId 发送会 LogPanic）。
func TestCreateActorRefExKeepsSystemId(t *testing.T) {
	ts := testutil.NewSystem(t, nil)
	ref := ts.CreateActorRefEx(5, 100, "a")
	if ref.GetSystemId() != 5 || ref.GetActorType() != 100 || ref.GetActorId() != "a" {
		t.Fatalf("ref fields wrong: %+v", ref)
	}
	ref2 := ts.CreateActorRef(100, "a")
	if ref2.GetSystemId() != 0 {
		t.Fatalf("CreateActorRef should leave SystemId 0, got %v", ref2.GetSystemId())
	}
}

func TestLogLevelString(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		level vactor.LogLevel
		want  string
	}{
		{vactor.DebugLevel, "DEBUG"},
		{vactor.InfoLevel, "INFO"},
		{vactor.WarnLevel, "WARN"},
		{vactor.ErrorLevel, "ERROR"},
		{vactor.FatalLevel, "FATAL"},
		{vactor.PanicLevel, "PANIC"},
		{vactor.LogLevel(99), "UNKNOWN"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.level.String(); got != tt.want {
				t.Fatalf("level %d string = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

func TestVAError(t *testing.T) {
	t.Parallel()

	t.Run("CarriesCode", func(t *testing.T) {
		err := vactor.NewVAError(vactor.ErrorCodeTimeout)
		if err.Code() != vactor.ErrorCodeTimeout {
			t.Fatalf("code = %v", err.Code())
		}
	})

	t.Run("TextContainsCode", func(t *testing.T) {
		for _, tt := range []struct {
			code vactor.ErrorCode
			want string
		}{
			{vactor.ErrorCodeSuccess, "VaError(code=0)"},
			{vactor.ErrorCodeTimeout, "VaError(code=1)"},
			{vactor.ErrorCodeInvalidActor, "VaError(code=2)"},
			{vactor.ErrorCodeSystemNotStarted, "VaError(code=3)"},
			{vactor.ErrorCodeHandlerPanic, "VaError(code=4)"},
			{vactor.ErrorCodeCustomStart, "VaError(code=100)"},
		} {
			if got := vactor.NewVAError(tt.code).Error(); got != tt.want {
				t.Fatalf("error text for code %d = %q, want %q", tt.code, got, tt.want)
			}
		}
	})

	t.Run("ImplementsError", func(t *testing.T) {
		var _ error = vactor.NewVAError(vactor.ErrorCodeTimeout)
	})
}

// 同步响应载荷为 nil（Response 字段缺失）：丢弃并记日志，不 panic。
func TestNilPayloadSyncResponseDropped(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(180, col.Creator())
	})
	ref := ts.CreateActorRef(180, "a")
	ts.Send(ref, "wake")
	col.WaitForMessages(t, 1, 2*time.Second, "actor activated")

	_ = ts.LocalRouter(&vactor.EnvelopeResponse{ToActorRef: ref, RequestId: 1, Response: nil})
	testutil.WaitFor(t, 2*time.Second, "nil payload response dropped with log", func() bool {
		return ts.LogContains("nil payload")
	})
	if !ts.IsRunning() {
		t.Fatal("system should survive a nil-payload response")
	}
}

// 向已回收 actor 投递迟到响应：不得重新激活它，且 actor 不得泄漏。
func TestSyncResponseToInactiveActorIsDropped(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(181, col.Creator())
		},
		testutil.WithTickInterval(5*time.Millisecond),
		testutil.WithStopInterval(30*time.Millisecond),
	)
	ref := ts.CreateActorRef(181, "a")
	ts.Send(ref, "wake")
	col.WaitForMessages(t, 1, 2*time.Second, "actor activated")
	// 等 idle 回收
	testutil.WaitFor(t, 2*time.Second, "actor recycled", func() bool { return col.Stops() >= 1 })
	starts := col.Starts()

	// 此时 actor 已回收，投递响应不应把它拉起来
	_ = ts.LocalRouter(&vactor.EnvelopeResponse{
		ToActorRef: ref,
		RequestId:  7,
		Response:   &vactor.Response{Message: "late"},
	})
	time.Sleep(300 * time.Millisecond)
	if col.Starts() != starts {
		t.Fatalf("actor was reactivated by a late response: starts %d -> %d", starts, col.Starts())
	}
}

// actor 回收后 mailbox 仍有积压：group 必须重建 context 继续消费，消息不丢。
func TestStoppedActorWithBacklogIsRebuilt(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(182, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					switch ctx.GetMessage().(type) {
					case *vactor.MsgOnStart:
						col.Observe(ctx)
					case string:
						// 立即具备回收条件：制造"回收后有积压"的窗口
						ctx.SetStopInterval(time.Nanosecond)
						col.Observe(ctx)
					}
				}
			})
		},
		testutil.WithTickInterval(3*time.Millisecond),
	)
	ref := ts.CreateActorRef(182, "a")
	testutil.WaitFor(t, 6*time.Second, "actor rebuilt from backlog", func() bool {
		for i := 0; i < 4; i++ {
			ts.Send(ref, "m")
		}
		return col.Starts() >= 2
	})
	if col.Len() == 0 {
		t.Fatal("backlogged messages were not consumed")
	}
}

// 两个"无目标 actor"的信封，GetToActorRef 必须返回 nil。
func TestEnvelopeNilToActorRef(t *testing.T) {
	if (&vactor.EnvelopeBatchSend{}).GetToActorRef() != nil {
		t.Fatal("EnvelopeBatchSend.GetToActorRef should be nil")
	}
	if (&vactor.EnvelopeNotify{}).GetToActorRef() != nil {
		t.Fatal("EnvelopeNotify.GetToActorRef should be nil")
	}
}

// 无发送方的异步请求：Response 必须归还计数，actor 仍可被闲置回收。
func TestAsyncRequestWithoutFromReleasesCount(t *testing.T) {
	const noFromType vactor.ActorType = 200
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(noFromType, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					if _, ok := ctx.GetMessage().(string); ok {
						ctx.Response("ok", nil)
					}
				}
			})
		},
		testutil.WithTickInterval(10*time.Millisecond),
		testutil.WithStopInterval(50*time.Millisecond),
	)
	_ = ts.LocalRouter(&vactor.EnvelopeRequestAsync{
		ToActorRef: ts.CreateActorRef(noFromType, "a"),
		Message:    "q",
		CallbackId: 1,
	})
	// actor 启动后 Response 走 fromActorRef == nil 分支；随后必须仍能闲置回收
	ts.Send(ts.CreateActorRef(noFromType, "a"), "wake")
	testutil.WaitFor(t, 3*time.Second, "actor survives and keeps processing", func() bool {
		return ts.LogContains("without a valid fromActorRef")
	})
}

// Response 重复调用：第二次必须被拒绝（覆盖同步/异步两条 doSendRsp 早退分支）。
func TestDoubleResponseRejectedForInnerRequests(t *testing.T) {
	const (
		dblCallerType    vactor.ActorType = 201
		dblResponderType vactor.ActorType = 202
	)
	type result struct {
		msg interface{}
		err vactor.VAError
	}
	syncCh := make(chan result, 2)
	asyncCh := make(chan result, 2)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(dblResponderType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				ctx.Response("first", nil)
				ctx.Response("second", nil) // 必须被拒绝
			}
		})
		s.RegisterActorType(dblCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				m, ok := ctx.GetMessage().(string)
				if !ok {
					return
				}
				ref := ctx.CreateActorRef(dblResponderType, "r")
				switch m {
				case "sync":
					msg, err := ctx.Request(ref, "q", 2*time.Second)
					syncCh <- result{msg, err}
				case "async":
					ctx.RequestAsync(ref, "q", 2*time.Second, func(msg interface{}, err vactor.VAError) {
						asyncCh <- result{msg, err}
					})
				}
			}
		})
	})

	ts.Send(ts.CreateActorRef(dblCallerType, "c"), "sync")
	sr := testutil.WaitChan(t, syncCh, 3*time.Second, "sync double response")
	if sr.err != nil || sr.msg != "first" {
		t.Fatalf("sync double response = (%v,%v), want (first,nil)", sr.msg, sr.err)
	}
	ts.Send(ts.CreateActorRef(dblCallerType, "c"), "async")
	ar := testutil.WaitChan(t, asyncCh, 3*time.Second, "async double response")
	if ar.err != nil || ar.msg != "first" {
		t.Fatalf("async double response = (%v,%v), want (first,nil)", ar.msg, ar.err)
	}
	if !ts.LogContains("more than once") {
		t.Fatal("expected duplicate-response log")
	}
}

// 已 Response 之后才 panic：respondErrorOnce 不得重复回错（覆盖三个早退分支）。
func TestPanicAfterResponseDoesNotDoubleRespond(t *testing.T) {
	const (
		lateCallerType vactor.ActorType = 203
		lateBoomType   vactor.ActorType = 204
	)
	type result struct {
		msg interface{}
		err vactor.VAError
	}
	syncCh := make(chan result, 2)
	asyncCh := make(chan result, 2)
	newBoom := func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			if _, ok := ctx.GetMessage().(string); !ok {
				return
			}
			ctx.Response("payload", nil)
			panic("boom after response")
		}
	}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(lateBoomType, newBoom)
		s.RegisterActorType(lateCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ref := ctx.CreateActorRef(lateBoomType, "lb")
					msg, err := ctx.Request(ref, "q", 2*time.Second)
					syncCh <- result{msg, err}
					ctx.RequestAsync(ref, "q", 2*time.Second, func(msg interface{}, err vactor.VAError) {
						asyncCh <- result{msg, err}
					})
				}
			}
		})
	})

	ts.Send(ts.CreateActorRef(lateCallerType, "c"), "go")
	sr := testutil.WaitChan(t, syncCh, 3*time.Second, "sync payload before panic")
	if sr.err != nil || sr.msg != "payload" {
		t.Fatalf("sync = (%v,%v), want (payload,nil)", sr.msg, sr.err)
	}
	ar := testutil.WaitChan(t, asyncCh, 3*time.Second, "async payload before panic")
	if ar.err != nil || ar.msg != "payload" {
		t.Fatalf("async = (%v,%v), want (payload,nil)", ar.msg, ar.err)
	}

	// 外部请求路径（覆盖 OuterRequest.respondErrorOnce 早退）
	rsp, err := ts.Request(ts.CreateActorRef(lateBoomType, "lb"), "q", 2*time.Second)
	if err != nil || rsp != "payload" {
		t.Fatalf("outer = (%v,%v), want (payload,nil)", rsp, err)
	}
}

// 异步响应携带未知的 CallbackId / 错误的 CallbackAddress：只告警，不得误匹配回调。
func TestAsyncResponseWithUnknownCallbackIgnored(t *testing.T) {
	const (
		cbCallerType vactor.ActorType = 205
		cbEchoType   vactor.ActorType = 206
	)
	got := make(chan string, 4)
	started := make(chan struct{}, 1)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		// 慢应答：保证 actor 的异步回调仍处于等待状态
		s.RegisterActorType(cbEchoType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				time.Sleep(400 * time.Millisecond)
				ctx.Response("late", nil)
			}
		})
		s.RegisterActorType(cbCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					started <- struct{}{}
					ctx.RequestAsync(ctx.CreateActorRef(cbEchoType, "e"), "q", 3*time.Second,
						func(msg interface{}, err vactor.VAError) {
							if err != nil {
								got <- "err"
								return
							}
							got <- "ok:" + msg.(string)
						})
				}
			}
		})
	})

	caller := ts.CreateActorRef(cbCallerType, "c")
	ts.Send(caller, "go")
	<-started
	time.Sleep(50 * time.Millisecond) // 让 RequestAsync 完成登记

	// CallbackId 不存在
	_ = ts.LocalRouter(&vactor.EnvelopeResponseAsync{
		ToActorRef: caller,
		Response:   &vactor.Response{Message: "x"},
		CallbackId: 999,
	})
	// CallbackId 存在但 CallbackAddress 不匹配
	_ = ts.LocalRouter(&vactor.EnvelopeResponseAsync{
		ToActorRef:      caller,
		Response:        &vactor.Response{Message: "x"},
		CallbackId:      1,
		CallbackAddress: 123456,
	})
	testutil.WaitFor(t, 2*time.Second, "unknown callback warned", func() bool {
		return ts.LogContains("unknown callbackId") && ts.LogContains("unknown callbackAddress")
	})
	// 真实响应仍必须正常回调
	if v := testutil.WaitChan(t, got, 3*time.Second, "real response still delivered"); v != "ok:late" {
		t.Fatalf("got %q, want ok:late", v)
	}
}

// actor 内 timeout=0 的同步请求：读到陈旧响应时丢弃并继续等待（覆盖无限等待循环）。
func TestUnboundedSyncRequestDropsStaleResponse(t *testing.T) {
	const (
		usCallerType vactor.ActorType = 207
		usEchoType   vactor.ActorType = 208
	)
	got := make(chan string, 4)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(usEchoType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				time.Sleep(120 * time.Millisecond)
				ctx.Response("echo", nil)
			}
		})
		s.RegisterActorType(usCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				ref := ctx.CreateActorRef(usEchoType, "e")
				// 第 1 次：超时离开，留下迟到响应占位
				if _, err := ctx.Request(ref, "q1", 20*time.Millisecond); err == nil {
					got <- "round1-unexpected-ok"
					return
				}
				time.Sleep(200 * time.Millisecond) // 等迟到响应进入通道
				// 第 2 次：无限等待，必须先丢弃陈旧响应再拿到真实响应
				msg, err := ctx.Request(ref, "q2", 0)
				if err != nil {
					got <- "round2-err"
					return
				}
				got <- msg.(string)
			}
		})
	})
	ts.Send(ts.CreateActorRef(usCallerType, "c"), "go")
	if v := testutil.WaitChan(t, got, 5*time.Second, "unbounded request after timeout"); v != "echo" {
		t.Fatalf("got %q, want echo", v)
	}
}
