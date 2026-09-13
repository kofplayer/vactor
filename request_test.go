package vactor_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

const (
	reqEchoType   vactor.ActorType = 110
	reqCallerType vactor.ActorType = 111
)

// 注册 echo actor：收到 string 请求后延迟 delay 毫秒再响应 "echo:<msg>"。
// noResp=true 时永不响应（用于超时类测试）。
func registerEchoReq(s vactor.System, delay time.Duration, noResp bool, fromSeen chan vactor.ActorRef) {
	s.RegisterActorType(reqEchoType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case string:
				if fromSeen != nil {
					fromSeen <- ctx.GetFromActorRef()
				}
				if noResp {
					return
				}
				if delay > 0 {
					time.Sleep(delay)
				}
				ctx.Response("echo:"+m, nil)
			}
		}
	})
}

func TestOuterRequestSuccess(t *testing.T) {
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 0, false, nil)
	})
	rsp, err := ts.Request(ts.CreateActorRef(reqEchoType, "1"), "hello", 2*time.Second)
	if err != nil || rsp != "echo:hello" {
		t.Fatalf("got (%v,%v)", rsp, err)
	}
}

func TestOuterRequestTimeout(t *testing.T) {
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 300*time.Millisecond, false, nil)
	})
	start := time.Now()
	_, err := ts.Request(ts.CreateActorRef(reqEchoType, "1"), "hello", 50*time.Millisecond)
	if err == nil || err.Code() != vactor.ErrorCodeTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestOuterRequestNoTimeoutBlocksUntilResponse(t *testing.T) {
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 100*time.Millisecond, false, nil)
	})
	result := make(chan string, 1)
	go func() {
		rsp, err := ts.Request(ts.CreateActorRef(reqEchoType, "1"), "hi", 0) // 0 = 无限等待
		if err != nil {
			result <- "ERR:" + err.Error()
		} else {
			result <- rsp.(string)
		}
	}()
	if got := testutil.WaitChan(t, result, 3*time.Second, "no-timeout request resolved"); got != "echo:hi" {
		t.Fatalf("got %q", got)
	}
}

func TestActorSyncRequestBetweenActors(t *testing.T) {
	fromSeen := make(chan vactor.ActorRef, 8)
	result := make(chan string, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 0, false, fromSeen)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					rsp, err := ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "hi", 2*time.Second)
					if err != nil {
						result <- "ERR:" + err.Error()
					} else {
						result <- rsp.(string)
					}
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	if got := testutil.WaitChan(t, result, 3*time.Second, "actor sync request"); got != "echo:hi" {
		t.Fatalf("got %q", got)
	}
	from := testutil.WaitChan(t, fromSeen, 2*time.Second, "echo saw caller")
	if from.GetActorType() != reqCallerType || from.GetActorId() != "c" {
		t.Fatalf("echo saw from = %v/%v", from.GetActorType(), from.GetActorId())
	}
}

func TestActorSyncRequestTimeout(t *testing.T) {
	result := make(chan string, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 300*time.Millisecond, false, nil)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					_, err := ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "hi", 50*time.Millisecond)
					if err == nil || err.Code() != vactor.ErrorCodeTimeout {
						result <- "WRONG"
					} else {
						result <- "TIMEOUT"
					}
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	if got := testutil.WaitChan(t, result, 3*time.Second, "actor sync timeout"); got != "TIMEOUT" {
		t.Fatalf("got %q", got)
	}
}

func TestRequestAsyncSuccess(t *testing.T) {
	result := make(chan string, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 0, false, nil)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.RequestAsync(ctx.CreateActorRef(reqEchoType, "1"), "hi", 2*time.Second,
						func(msg interface{}, err vactor.VAError) {
							if err != nil {
								result <- "ERR"
							} else {
								result <- msg.(string)
							}
						})
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	if got := testutil.WaitChan(t, result, 3*time.Second, "async callback"); got != "echo:hi" {
		t.Fatalf("got %q", got)
	}
}

// 异步请求超时通过 tick 检查触发，回调收到 ErrorCodeTimeout。
func TestRequestAsyncTimeoutViaTick(t *testing.T) {
	result := make(chan vactor.ErrorCode, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 0, true, nil) // 永不响应
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.RequestAsync(ctx.CreateActorRef(reqEchoType, "1"), "hi", 100*time.Millisecond,
						func(msg interface{}, err vactor.VAError) {
							if err != nil {
								result <- err.Code()
							} else {
								result <- 0
							}
						})
				}
			}
		})
	}, testutil.WithTickInterval(20*time.Millisecond))
	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	if code := testutil.WaitChan(t, result, 3*time.Second, "async timeout callback"); code != vactor.ErrorCodeTimeout {
		t.Fatalf("got code %v", code)
	}
}

// 重复 Response：第一次生效并记录错误日志，不崩溃。
func TestDoubleResponseFirstWins(t *testing.T) {
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(reqEchoType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Response("first", nil)
					ctx.Response("second", nil)
				}
			}
		})
	})
	rsp, err := ts.Request(ts.CreateActorRef(reqEchoType, "1"), "hi", 2*time.Second)
	if err != nil || rsp != "first" {
		t.Fatalf("got (%v,%v)", rsp, err)
	}
	testutil.WaitFor(t, 2*time.Second, "double response logged", func() bool {
		return ts.LogContains("more than once")
	})
}

// 对 Send 消息调用 Response：只记错误日志，不崩溃。
func TestResponseNotAllowedOnSend(t *testing.T) {
	col := &testutil.Collector{}
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Response("oops", nil)
				}
				col.Observe(ctx)
			}
		})
	})
	ts.Send(ts.CreateActorRef(100, "a"), "x")
	col.WaitForMessages(t, 1, 2*time.Second, "message processed")
	testutil.WaitFor(t, 2*time.Second, "not-allowed logged", func() bool {
		return ts.LogContains("not allowed")
	})
}

// RequestAsync 在发送失败（router 返回错误）时立即以错误回调，不等待超时。
func TestRequestAsyncImmediateErrorCallback(t *testing.T) {
	result := make(chan vactor.ErrorCode, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		s.RegisterActorType(100, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.RequestAsync(ctx.CreateActorRef(101, "bad"), "q", 30*time.Second,
						func(msg interface{}, err vactor.VAError) {
							if err != nil {
								result <- err.Code()
							} else {
								result <- 0
							}
						})
				}
			}
		})
		// 启动前注入 router：对目标 "bad" 的异步请求返回发送失败
		s.SetRouter(func(envelope vactor.Envelope) vactor.VAError {
			if req, ok := envelope.(*vactor.EnvelopeRequestAsync); ok &&
				req.ToActorRef != nil && req.ToActorRef.GetActorId() == "bad" {
				return vactor.NewVAError(123)
			}
			return s.LocalRouter(envelope)
		})
	})
	ts.Send(ts.CreateActorRef(100, "a"), "go")
	if code := testutil.WaitChan(t, result, 2*time.Second, "immediate error callback"); code != 123 {
		t.Fatalf("got code %v", code)
	}
}

// 同步 Request 的响应会阻塞本 actor goroutine 但不影响其他 actor。
func TestSyncRequestBlocksOnlyCaller(t *testing.T) {
	callerDone := make(chan bool, 1)
	otherSeen := make(chan bool, 1)
	var gate atomic.Bool
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 200*time.Millisecond, false, nil)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "hi", 3*time.Second)
					callerDone <- true
				}
			}
		})
		s.RegisterActorType(112, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					otherSeen <- true
				}
			}
		})
	}, testutil.WithGroupCount(1)) // 同一 group 也互不影响

	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	// caller 阻塞期间，其他 actor 正常处理
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !gate.Load() {
		ts.Send(ts.CreateActorRef(112, "o"), "ping")
		select {
		case <-otherSeen:
			gate.Store(true)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !gate.Load() {
		t.Fatal("other actor should process while caller blocks in sync request")
	}
	testutil.WaitChan(t, callerDone, 3*time.Second, "caller finished")
}

// 行为断言：一次同步请求超时之后，该 actor 后续的同步请求仍必须正常拿到响应。
// 保护点：syncRspChan 容量为 1，投递新响应前会先排空残留的陈旧响应，避免有效
// 响应被 select-default 挤掉。
// 说明：请求方进入等待时会自行读走并丢弃陈旧响应，因此"占位"的实际触发窗口
// 很窄（响应需在请求方进入 select 之前到达）。本测试主要防止后续改动破坏
// "超时之后可恢复"这一语义。
func TestActorSyncRequestRecoversAfterTimeout(t *testing.T) {
	result := make(chan string, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 300*time.Millisecond, false, nil)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				switch ctx.GetMessage().(type) {
				case string:
					ref := ctx.CreateActorRef(reqEchoType, "1")
					// 第 1 轮：超时 50ms 远小于 echo 的 300ms 延迟 -> 必定超时
					if _, err := ctx.Request(ref, "r1", 50*time.Millisecond); err == nil {
						result <- "ROUND1_UNEXPECTED_OK"
						return
					}
					// 等迟到的响应落进响应通道（占位）
					time.Sleep(400 * time.Millisecond)
					// 第 2 轮：给足 2s，echo 只延迟 300ms，本应成功
					rsp, err := ctx.Request(ref, "r2", 2*time.Second)
					if err != nil {
						result <- "ROUND2_ERR:" + err.Error()
						return
					}
					result <- rsp.(string)
				}
			}
		})
	})
	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	if got := testutil.WaitChan(t, result, 8*time.Second, "sync request after a previous timeout"); got != "echo:r2" {
		t.Fatalf("got %q", got)
	}
}

// 回归测试：向已回收的 actor 投递迟到响应，不得重新激活它（否则留下僵尸 actor）。
func TestLateResponseDoesNotReactivateStoppedActor(t *testing.T) {
	const targetType vactor.ActorType = 112
	starts := make(chan struct{}, 8)
	ts := testutil.NewSystem(t,
		func(s vactor.System) {
			// echo：延迟 200ms 应答，保证响应在调用方超时之后才到达
			registerEchoReq(s, 200*time.Millisecond, false, nil)
			s.RegisterActorType(targetType, func() vactor.Actor {
				return func(ctx vactor.EnvelopeContext) {
					switch ctx.GetMessage().(type) {
					case *vactor.MsgOnStart:
						starts <- struct{}{}
					case string:
						// 超时 50ms，echo 200ms 后才应答 -> 响应必定迟到
						ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "late", 50*time.Millisecond)
						// 主动把自己标记为立即回收
						ctx.SetStopInterval(30 * time.Millisecond)
					}
				}
			})
		},
		testutil.WithTickInterval(10*time.Millisecond),
	)
	ts.Send(ts.CreateActorRef(targetType, "t"), "go")
	testutil.WaitChan(t, starts, 3*time.Second, "target started")
	// 等待：actor 被回收 + 迟到响应到达（若被重新激活会再产生一次 MsgOnStart）
	testutil.NoReceive(t, starts, 1200*time.Millisecond, "actor must not be reactivated by a late response")
}

// actor 内同步请求的超时参数为 0（无限等待）时的成功路径。
func TestActorSyncRequestUnboundedWait(t *testing.T) {
	result := make(chan string, 8)
	ts := testutil.NewSystem(t, func(s vactor.System) {
		registerEchoReq(s, 50*time.Millisecond, false, nil)
		s.RegisterActorType(reqCallerType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(string); !ok {
					return
				}
				rsp, err := ctx.Request(ctx.CreateActorRef(reqEchoType, "1"), "hi", 0)
				if err != nil {
					result <- "ERR:" + err.Error()
					return
				}
				result <- rsp.(string)
			}
		})
	})
	ts.Send(ts.CreateActorRef(reqCallerType, "c"), "go")
	if got := testutil.WaitChan(t, result, 3*time.Second, "unbounded actor sync request"); got != "echo:hi" {
		t.Fatalf("got %q", got)
	}
}
