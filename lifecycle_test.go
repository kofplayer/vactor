package vactor_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

func TestStartStopLifecycle(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	})
	if !ts.IsRunning() {
		t.Fatal("system should be running after Start")
	}
	ts.Send(ts.CreateActorRef(100, "a"), "m1")
	col.WaitForMessages(t, 1, 2*time.Second, "m1 delivered")

	ts.Stop()
	if ts.IsRunning() {
		t.Fatal("system should not be running after Stop")
	}
	if col.Stops() != 1 {
		t.Fatalf("MsgOnStop delivered %d times, want 1", col.Stops())
	}
	ts.Stop() // 幂等
}

func TestRegisterActorTypeValidation(t *testing.T) {
	col := &testutil.Collector{}
	// 类型校验只在启动前生效，放到 setup 阶段。
	// 注意：Start 之前的日志走默认 stdout logger（config.LogFunc 到 Start 才生效），
	// 因此这里不断言日志，而是用"类型确实未被注册"的后续行为验证拒绝生效。
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(5, col.Creator()) // < ActorTypeStart(10)
	})

	// 启动后注册：拒绝且不 panic
	ts.RegisterActorType(100, col.Creator())
	if !ts.LogContains("cannot change config after system started") {
		t.Fatal("expected error log for register after start")
	}

	// 被拒绝的类型 5 未注册：向它发消息应记录 can not find actor type
	testutil.WaitFor(t, 3*time.Second, "type 5 was rejected", func() bool {
		ts.Send(ts.CreateActorRef(5, "x"), "ping")
		return ts.LogContains("can not find actor type 5")
	})

	// 未注册类型：Request 超时、不 panic
	rsp, err := ts.Request(ts.CreateActorRef(999, "x"), "ping", 300*time.Millisecond)
	if rsp != nil || err == nil || err.Code() != vactor.ErrorCodeTimeout {
		t.Fatalf("request to unregistered type should timeout, got (%v,%v)", rsp, err)
	}
	testutil.WaitFor(t, 2*time.Second, "log unknown actor type", func() bool {
		return ts.LogContains("can not find actor type")
	})
}

func TestTickDelivered(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	}, testutil.WithTickInterval(20*time.Millisecond))
	ts.Send(ts.CreateActorRef(100, "a"), "boot")
	testutil.WaitFor(t, 2*time.Second, "ticks delivered", func() bool {
		return col.Ticks() >= 3
	})
}

func TestTickDisabled(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, col.Creator())
	}, testutil.WithTickInterval(0))
	ts.Send(ts.CreateActorRef(100, "a"), "m1")
	col.WaitForMessages(t, 1, 2*time.Second, "message works without ticker")
	time.Sleep(150 * time.Millisecond)
	if col.Ticks() != 0 {
		t.Fatalf("tick disabled but got %d ticks", col.Ticks())
	}
}

func TestIdleRecycleAndReactivation(t *testing.T) {
	var starts, stops atomic.Int32
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					starts.Add(1)
				case *vactor.MsgOnStop:
					stops.Add(1)
				case string:
					// 业务消息
				}
			}
		})
	}, testutil.WithStopInterval(300*time.Millisecond))

	ref := ts.CreateActorRef(100, "a")
	ts.Send(ref, "m1")
	testutil.WaitFor(t, 2*time.Second, "first activation", func() bool { return starts.Load() >= 1 })

	// 闲置 300ms 后应由 tick 回收
	testutil.WaitFor(t, 3*time.Second, "idle recycle", func() bool { return stops.Load() >= 1 })

	// 虚拟 actor：再次发消息即重新激活
	ts.Send(ref, "m2")
	testutil.WaitFor(t, 2*time.Second, "reactivation", func() bool { return starts.Load() >= 2 })
	if ts.IsRunning() == false {
		t.Fatal("system still running")
	}
}

// watcher 关系在 actor 闲置回收后通过 cache 保留：回收再激活后仍能收到通知。
func TestIdleRecyclePreservesInnerWatchers(t *testing.T) {
	const wt = vactor.WatchType(7)
	notifySeen := make(chan string, 8)
	var targetStops atomic.Int32
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor { // watcher
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.Watch(ctx.CreateActorRef(101, "target"), wt)
				case *vactor.MsgOnWatchMsg:
					notifySeen <- "watch"
				}
			}
		})
		s.RegisterActorType(101, func() vactor.Actor { // target
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStop:
					targetStops.Add(1)
				case string:
					ctx.Notify(wt, "hello")
				}
			}
		})
	}, testutil.WithStopInterval(300*time.Millisecond))

	ts.Send(ts.CreateActorRef(100, "w1"), "boot")
	settle()                                        // 等 watcher 的 watch 信封送达 target
	ts.Send(ts.CreateActorRef(101, "target"), "go") // 第一次通知
	testutil.WaitChan(t, notifySeen, 3*time.Second, "first notify")

	// target 闲置回收
	testutil.WaitFor(t, 3*time.Second, "target recycled", func() bool {
		return targetStops.Load() >= 1
	})

	// 再激活后 watcher 关系仍在（cache 生效）
	ts.Send(ts.CreateActorRef(101, "target"), "go")
	testutil.WaitChan(t, notifySeen, 3*time.Second, "notify after recycle (watcher cache preserved)")
}

// 外部 Queue watcher 的订阅关系同样在回收后保留。
func TestIdleRecyclePreservesOuterWatchers(t *testing.T) {
	const wt = vactor.WatchType(8)
	queue := vactor.NewQueue[interface{}]()
	seen := make(chan *vactor.MsgOnWatchMsg, 8)
	var targetStops atomic.Int32
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStop:
					targetStops.Add(1)
				case string:
					ctx.Notify(wt, "hello")
				}
			}
		})
	}, testutil.WithStopInterval(300*time.Millisecond))

	ts.Watch(ts.CreateActorRef(101, "target"), wt, queue)
	go func() {
		for {
			m, ok := queue.Dequeue()
			if !ok {
				return
			}
			if msg, ok := m.(*vactor.MsgOnWatchMsg); ok {
				seen <- msg
			}
		}
	}()

	ts.Send(ts.CreateActorRef(101, "target"), "go")
	testutil.WaitChan(t, seen, 3*time.Second, "first outer notify")

	testutil.WaitFor(t, 3*time.Second, "target recycled", func() bool {
		return targetStops.Load() >= 1
	})

	ts.Send(ts.CreateActorRef(101, "target"), "go")
	testutil.WaitChan(t, seen, 3*time.Second, "outer notify after recycle (cache preserved)")
}

func TestSetStopIntervalZeroNeverRecycles(t *testing.T) {
	var stops atomic.Int32
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					ctx.SetStopInterval(0) // 永不回收
				case *vactor.MsgOnStop:
					stops.Add(1)
				}
				col.Observe(ctx)
			}
		})
	}, testutil.WithStopInterval(100*time.Millisecond))

	ref := ts.CreateActorRef(100, "a")
	ts.Send(ref, "boot")
	col.WaitForMessages(t, 1, 2*time.Second, "activated")
	time.Sleep(600 * time.Millisecond) // 远超系统默认 100ms
	if stops.Load() != 0 {
		t.Fatal("SetStopInterval(0) should prevent idle recycle")
	}
	ts.Send(ref, "still-alive")
	col.WaitForMessages(t, 2, 2*time.Second, "actor still processing")
}

func TestSetSelfInvalid(t *testing.T) {
	var starts, stops, processed atomic.Int32
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch m := ctx.GetMessage().(type) {
				case *vactor.MsgOnStart:
					starts.Add(1)
				case *vactor.MsgOnStop:
					stops.Add(1)
				case string:
					if m == "die" {
						ctx.SetSelfInvalid()
						return
					}
					processed.Add(1)
				}
			}
		})
	})
	ref := ts.CreateActorRef(100, "v")

	ts.Send(ref, "die")
	// 失效期间（回收前的 1 秒窗口内）Request 应立即得到 ErrorCodeInvalidActor
	_, err := ts.Request(ref, "ping", 2*time.Second)
	if err == nil || err.Code() != vactor.ErrorCodeInvalidActor {
		t.Fatalf("request to invalid actor should fail fast with ErrorCodeInvalidActor, got %v", err)
	}

	// 失效 actor 的 MsgOnStop 同样被吞掉（processMessage 对 isInvalid 短路），
	// 因此无法用 stops 计数观察回收；先等 1 秒回收窗口过去，再验证重新激活
	// （每个 context 只会 OnStart 一次，starts >= 2 即代表旧 context 已回收）。
	if processed.Load() != 0 {
		t.Fatal("invalid actor must not process messages")
	}
	time.Sleep(1500 * time.Millisecond)
	ts.Send(ref, "boom")
	testutil.WaitFor(t, 4*time.Second, "recycled and reactivated", func() bool { return starts.Load() >= 2 })
	testutil.WaitFor(t, 2*time.Second, "processed after reactivation", func() bool {
		return processed.Load() >= 1
	})
}

// 未完成（未 Response）的入向请求会把 actor 挂住不回收；回调超时后恢复回收。
func TestPendingRequestPreventsRecycle(t *testing.T) {
	var targetStops atomic.Int32
	ts := testutil.NewSystem(t, func(s vactor.System) {
		// 101：永不回应的 echo
		s.RegisterActorType(101, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {}
		})
		// 100：收到 "work" 后发起一个 1.5s 超时的异步请求
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case *vactor.MsgOnStop:
					targetStops.Add(1)
				case string:
					if ctx.GetMessage().(string) == "work" {
						ctx.RequestAsync(ctx.CreateActorRef(101, "x"), "q", 1500*time.Millisecond,
							func(interface{}, vactor.VAError) {})
					}
				}
			}
		})
	}, testutil.WithStopInterval(200*time.Millisecond))

	ts.Send(ts.CreateActorRef(100, "a"), "work")

	// 有未完成请求时不得闲置回收（默认回收间隔 200ms，此处等 600ms 验证未回收）
	time.Sleep(600 * time.Millisecond)
	if targetStops.Load() != 0 {
		t.Fatal("actor must not recycle while a request is pending")
	}

	// 异步回调超时（1.5s）后恢复闲置回收
	testutil.WaitFor(t, 6*time.Second, "recycled after callback timeout", func() bool {
		return targetStops.Load() >= 1
	})
}

// 回归测试：响应无处可投（请求没有发送方）时，请求计数仍必须归还。
// 修复前 Response 提前 return 跳过了 processingRequestCount--，
// 该 actor 从此永远满足不了回收条件（永久泄漏）。
func TestUndeliverableResponseReleasesRequestCount(t *testing.T) {
	const noFromType vactor.ActorType = 130
	var stops int32
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(noFromType, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					switch ctx.GetMessage().(type) {
					case *vactor.MsgOnStop:
						atomic.AddInt32(&stops, 1)
					case string:
						ctx.Response("ok", nil)
					}
				}
			})
		},
		testutil.WithTickInterval(10*time.Millisecond),
		testutil.WithStopInterval(60*time.Millisecond),
	)
	ref := ts.CreateActorRef(noFromType, "a")
	ts.Send(ref, "wake") // 激活 actor
	// 投递一个没有发送方的请求：actor 应答时 fromActorRef 为 nil
	if err := ts.LocalRouter(&vactor.EnvelopeRequest{ToActorRef: ref, Message: "req", RequestId: 1}); err != nil {
		t.Fatalf("LocalRouter: %v", err)
	}
	testutil.WaitFor(t, 3*time.Second, "actor recycled after an undeliverable response", func() bool {
		return atomic.LoadInt32(&stops) >= 1
	})
}

// SetSelfInvalid 之后，三类请求（外部 Request、actor 间 Request、RequestAsync）
// 都必须立即收到 ErrorCodeInvalidActor（覆盖 processMessage 的三条拒绝分支）。
func TestSelfInvalidRespondsToAllRequestKinds(t *testing.T) {
	const (
		invalidType vactor.ActorType = 170
		peerType    vactor.ActorType = 171
	)
	const noErr = vactor.ErrorCode(-1)
	innerSync := make(chan vactor.ErrorCode, 4)
	innerAsync := make(chan vactor.ErrorCode, 4)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(invalidType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); ok {
					ctx.SetSelfInvalid()
				}
			}
		})
		s.RegisterActorType(peerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				m, ok := ctx.GetMessage().(string)
				if !ok {
					return
				}
				ref := ctx.CreateActorRef(invalidType, "v")
				switch m {
				case "sync":
					if _, err := ctx.Request(ref, "q", 2*time.Second); err == nil {
						innerSync <- noErr
					} else {
						innerSync <- err.Code()
					}
				case "async":
					ctx.RequestAsync(ref, "q", 2*time.Second, func(_ interface{}, err vactor.VAError) {
						if err == nil {
							innerAsync <- noErr
						} else {
							innerAsync <- err.Code()
						}
					})
				}
			}
		})
	})

	target := ts.CreateActorRef(invalidType, "v")
	ts.Send(target, "invalidate")
	time.Sleep(150 * time.Millisecond) // 等 SetSelfInvalid 生效

	// 外部同步请求
	if _, err := ts.Request(target, "q", 2*time.Second); err == nil || err.Code() != vactor.ErrorCodeInvalidActor {
		t.Fatalf("outer request: got %v, want ErrorCodeInvalidActor", err)
	}
	// actor 间同步请求
	ts.Send(ts.CreateActorRef(peerType, "p"), "sync")
	if got := testutil.WaitChan(t, innerSync, 3*time.Second, "peer sync"); got != vactor.ErrorCodeInvalidActor {
		t.Fatalf("inner sync got %v", got)
	}
	// actor 间异步请求
	ts.Send(ts.CreateActorRef(peerType, "p"), "async")
	if got := testutil.WaitChan(t, innerAsync, 3*time.Second, "peer async"); got != vactor.ErrorCodeInvalidActor {
		t.Fatalf("inner async got %v", got)
	}
}

// mailbox 深度上限：慢消费者导致积压时，超过上限的消息被丢弃并记 Error，
// 而不是让 mailbox 无界增长。
func TestActorMailboxDepthLimitDropsExcess(t *testing.T) {
	const limit = 4
	release := make(chan struct{})
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(220, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					if _, ok := ctx.GetMessage().(string); !ok {
						return
					}
					<-release // 阻塞，制造 mailbox 积压
					col.Observe(ctx)
				}
			})
		},
		testutil.WithMaxMailboxDepth(limit),
		testutil.WithTickInterval(10*time.Millisecond),
	)
	ref := ts.CreateActorRef(220, "a")
	ts.Send(ref, "first")
	time.Sleep(80 * time.Millisecond) // 让 actor 取走第一条并阻塞

	for i := 0; i < 30; i++ {
		ts.Send(ref, i)
	}
	testutil.WaitFor(t, 3*time.Second, "mailbox limit logged", func() bool {
		return ts.LogContains("reached limit")
	})
	close(release)
	// 被丢弃的消息不会进入 actor：总数不超过 上限 + 1（已在处理中的那条）
	testutil.WaitFor(t, 3*time.Second, "actor drains remaining backlog", func() bool {
		return col.Len() >= 1
	})
	time.Sleep(200 * time.Millisecond)
	if got := col.Len(); got > limit+1 {
		t.Fatalf("actor received %d messages, want <= %d (limit+1)", got, limit+1)
	}
	if !ts.IsRunning() {
		t.Fatal("system should survive mailbox drops")
	}
}

// mailbox 高水位：只告警不丢弃（消息全部送达）。
func TestActorMailboxHighWaterMarkWarnsOnly(t *testing.T) {
	const mark = 2
	release := make(chan struct{})
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(221, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					if _, ok := ctx.GetMessage().(string); !ok {
						return
					}
					<-release
					col.Observe(ctx)
				}
			})
		},
		testutil.WithMailboxHighWaterMark(mark),
		testutil.WithTickInterval(10*time.Millisecond),
	)
	ref := ts.CreateActorRef(221, "a")
	ts.Send(ref, "first")
	time.Sleep(80 * time.Millisecond)
	for i := 0; i < 6; i++ {
		ts.Send(ref, "m") // 必须与 actor 约定的消息类型一致（string），否则被忽略
	}
	testutil.WaitFor(t, 3*time.Second, "high water mark warned", func() bool {
		return ts.LogContains("exceeds high water mark")
	})
	close(release)
	// 全部消息都必须送达（高水位不丢弃）
	testutil.WaitFor(t, 3*time.Second, "all messages delivered", func() bool {
		return col.Len() >= 7
	})
}

// SetTickEnabled(false)：纯空闲 actor 不再被每秒唤醒（收不到 MsgOnTick）。
func TestSetTickEnabledSkipsIdleTicks(t *testing.T) {
	ticks := make(chan struct{}, 64)
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(230, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					switch ctx.GetMessage().(type) {
					case *vactor.MsgOnStart:
						ctx.SetTickEnabled(false)
					case *vactor.MsgOnTick:
						select {
						case ticks <- struct{}{}:
						default:
						}
					}
				}
			})
		},
		testutil.WithTickInterval(10*time.Millisecond),
	)
	ts.Send(ts.CreateActorRef(230, "a"), "wake")
	time.Sleep(200 * time.Millisecond) // 等 actor 启动并声明关闭 tick
	// 关闭后 10ms 一个 tick，200ms 窗口内本应收到约 20 个
	testutil.NoReceive(t, ticks, 200*time.Millisecond, "idle actor should not receive ticks")
	if !ts.IsRunning() {
		t.Fatal("system should stay running")
	}
}

// 关闭 tick 不会破坏框架自身依赖 tick 的能力：未完成的异步请求仍会被扫描超时。
func TestSetTickEnabledStillScansAsyncTimeout(t *testing.T) {
	got := make(chan string, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(231, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(*vactor.MsgOnStart); !ok {
					return
				}
				ctx.SetTickEnabled(false)
				// 目标 actor 永不响应，回调超时后必须被框架扫出来
				ctx.RequestAsync(ctx.CreateActorRef(232, "void"), "q", 50*time.Millisecond,
					func(_ interface{}, err vactor.VAError) {
						if err != nil && err.Code() == vactor.ErrorCodeTimeout {
							got <- "async-timeout"
							return
						}
						got <- "unexpected"
					})
			}
		})
		s.RegisterActorType(232, func() vactor.Actor {
			return func(vactor.EnvelopeContext) {}
		})
	}, testutil.WithTickInterval(10*time.Millisecond))

	ts.Send(ts.CreateActorRef(231, "a"), "go")
	if v := testutil.WaitChan(t, got, 3*time.Second, "async callback timeout"); v != "async-timeout" {
		t.Fatalf("got %q", v)
	}
}

// 关闭 tick 的 actor 若设置了闲置回收间隔，到期后仍会被回收（tick 照常投递）。
func TestSetTickEnabledStillRecyclesIdleActor(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(233, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					switch ctx.GetMessage().(type) {
					case *vactor.MsgOnStart:
						col.Observe(ctx)
						ctx.SetTickEnabled(false)
						ctx.SetStopInterval(60 * time.Millisecond)
					case *vactor.MsgOnStop:
						col.Observe(ctx)
					}
				}
			})
		},
		testutil.WithTickInterval(10*time.Millisecond),
	)
	ts.Send(ts.CreateActorRef(233, "a"), "wake")
	testutil.WaitFor(t, 3*time.Second, "idle actor recycled despite tick disabled", func() bool {
		return col.Stops() >= 1
	})
}

// TickInterval<=0 却设置了闲置回收时间，属于自相矛盾的配置：tick 是回收检查与
// 异步超时扫描的唯一时机，该组合下回收永不生效。Start 必须记 Warn 提示。
func TestStartWarnsWhenTickDisabledButStopIntervalSet(t *testing.T) {
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(240, func() vactor.Actor { return func(vactor.EnvelopeContext) {} })
		},
		testutil.WithTickInterval(0),
		testutil.WithStopInterval(time.Minute),
	)
	if !ts.LogContains("disables the tick loop") {
		t.Fatal("expected a warning when TickInterval<=0 while DefaultStopInterval>0")
	}
	if !ts.IsRunning() {
		t.Fatal("conflicting config should still start the system")
	}

	// 反向：tick 开启时不出现该告警（同一套断言必须能区分两种配置）
	ts2 := testutil.NewSystem(t,
		func(s vactor.System) {
			s.RegisterActorType(241, func() vactor.Actor { return func(vactor.EnvelopeContext) {} })
		},
		testutil.WithTickInterval(10*time.Millisecond),
		testutil.WithStopInterval(time.Minute),
	)
	if ts2.LogContains("disables the tick loop") {
		t.Fatal("no warning expected when the tick loop is enabled")
	}
}
