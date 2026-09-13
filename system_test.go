package vactor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 注册一个 echo actor：收到 string 后延迟 delay 再响应
func registerEchoActor(system System, actorType ActorType, delay time.Duration) {
	system.RegisterActorType(actorType, func() Actor {
		return func(ctx EnvelopeContext) {
			switch m := ctx.GetMessage().(type) {
			case string:
				if delay > 0 {
					time.Sleep(delay)
				}
				ctx.Response("echo:"+m, nil)
			}
		}
	})
}

func TestSyncRequestResponse(t *testing.T) {
	system := NewSystem()
	echoType := ActorTypeStart + 1
	registerEchoActor(system, echoType, 0)
	system.Start()
	defer system.Stop()

	rsp, err := system.Request(system.CreateActorRef(echoType, "1"), "hello", time.Second)
	if err != nil {
		t.Fatalf("request error: %v", err.Code())
	}
	if rsp != "echo:hello" {
		t.Fatalf("unexpected response: %v", rsp)
	}
}

func TestSyncRequestTimeout(t *testing.T) {
	system := NewSystem()
	echoType := ActorTypeStart + 1
	registerEchoActor(system, echoType, 300*time.Millisecond)
	system.Start()
	defer system.Stop()

	_, err := system.Request(system.CreateActorRef(echoType, "1"), "hello", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if err.Code() != ErrorCodeTimeout {
		t.Fatalf("expected ErrorCodeTimeout, got %v", err.Code())
	}
}

// 回归测试：同步 Request 超时后，迟到的旧响应不得被下一个无关 Request 消费
func TestStaleSyncResponseDropped(t *testing.T) {
	system := NewSystem()
	echoType := ActorTypeStart + 1
	callerType := ActorTypeStart + 2
	registerEchoActor(system, echoType, 300*time.Millisecond)

	done := make(chan interface{}, 1)
	system.RegisterActorType(callerType, func() Actor {
		return func(ctx EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case *MsgOnStart:
				echoRef := ctx.CreateActorRef(echoType, "1")
				// 第一次请求：50ms 超时（echo 300ms 才回）
				_, err := ctx.Request(echoRef, "first", 50*time.Millisecond)
				if err == nil || err.Code() != ErrorCodeTimeout {
					done <- "first request should timeout"
					return
				}
				time.Sleep(100 * time.Millisecond)
				// 第二次请求：旧响应（first）此时到达，必须被丢弃
				rsp, err := ctx.Request(echoRef, "second", 3*time.Second)
				if err != nil {
					done <- "second request error: " + err.Error()
					return
				}
				done <- rsp
			}
		}
	})
	system.Start()
	defer system.Stop()

	system.Send(system.CreateActorRef(callerType, "1"), "go")

	select {
	case rsp := <-done:
		if rsp != "echo:second" {
			t.Fatalf("stale response leaked into next request, got: %v", rsp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test timeout")
	}
}

// 回归测试：TickInterval<=0 时 Stop 不得 panic；Stop 可重复调用
func TestStopWithoutTickerAndDoubleStop(t *testing.T) {
	system := NewSystem(func(c *SystemConfig) {
		c.TickInterval = 0
	})
	system.Start()
	system.Stop()
	system.Stop() // 幂等
	if system.IsRunning() {
		t.Fatal("IsRunning should be false after Stop")
	}
}

func TestRingBufferClearZeroesSlots(t *testing.T) {
	rb := NewRingBuffer[*int](4)
	a, b := 1, 2
	rb.Push(&a)
	rb.Push(&b)
	rb.Clear()
	if rb.Count() != 0 || !rb.IsEmpty() {
		t.Fatal("Clear should empty the buffer")
	}
	for i := 0; i < rb.Size(); i++ {
		if rb.buffer[i] != nil {
			t.Fatalf("Clear should zero slot %d", i)
		}
	}
}

// 回归测试：系统停止后本地投递必须返回错误。
// 修复前 LocalRouter 忽略 Enqueue 的失败返回值，停机期间的消息被静默丢弃，
// 调用方（BatchSend/Request）却以为发送成功。
func TestLocalRouterAfterStopReturnsError(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	echoType := ActorTypeStart + 1
	registerEchoActor(system, echoType, 0)
	system.Start()
	system.Stop()

	ref := system.CreateActorRef(echoType, "a")
	if err := system.LocalRouter(&EnvelopeSend{ToActorRef: ref, Message: "x"}); err == nil {
		t.Fatal("LocalRouter should report delivery failure after Stop")
	}
}

// Start 之后的配置类调用必须被拒绝（只记日志，不得生效）。
func TestConfigGuardsRejectAfterStart(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	targetType := ActorTypeStart + 1
	system.RegisterActorType(targetType, func() Actor {
		return func(EnvelopeContext) {}
	})
	system.Start()
	defer system.Stop()

	// 三者都应在 Start 之后被拒绝：若 SetRouter(nil) 生效，BatchSend 会因 router 为 nil 而报错
	system.SetRouter(nil)
	system.SetCreateActorRefExFunc(nil)
	system.RegisterActorType(ActorTypeStart+2, func() Actor { return func(EnvelopeContext) {} })

	ref := system.CreateActorRef(targetType, "a")
	if ref == nil {
		t.Fatal("CreateActorRef should keep working after rejected config calls")
	}
	if err := system.BatchSend([]ActorRef{ref}, []interface{}{"x"}); err != nil {
		t.Fatalf("BatchSend failed, router may have been overwritten: %v", err)
	}
}

// Watch/Unwatch 传 nil queue 时静默忽略：不 panic、不注册）。
func TestWatchNilQueueIgnored(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	watchType := ActorTypeStart + 1
	registerEchoActor(system, watchType, 0)
	system.Start()
	defer system.Stop()

	ref := system.CreateActorRef(watchType, "a")
	system.Watch(ref, WatchType(1), nil)   // 覆盖 queue == nil 分支
	system.Unwatch(ref, WatchType(1), nil) // 覆盖 queue == nil 分支
	system.Send(ref, "still-works")
	time.Sleep(50 * time.Millisecond)
	if !system.IsRunning() {
		t.Fatal("system should stay running")
	}
}

// Start 之前调用 LocalRouter 必须返回错误而不是 panic。
func TestLocalRouterBeforeStartReturnsError(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
	})
	defer system.Stop()
	ref := system.CreateActorRef(ActorTypeStart+1, "a")
	if err := system.LocalRouter(&EnvelopeSend{ToActorRef: ref, Message: "x"}); err == nil {
		t.Fatal("LocalRouter before Start should return an error")
	}
}

// 内部信封（不外发）的 GetToActorRef 必须返回 nil。
func TestEnvelopeInternalGetToActorRef(t *testing.T) {
	if (&envelopeStopedReport{}).GetToActorRef() != nil {
		t.Fatal("envelopeStopedReport.GetToActorRef should be nil")
	}
	if (&envelopeTick{}).GetToActorRef() != nil {
		t.Fatal("envelopeTick.GetToActorRef should be nil")
	}
}

// System 级别的 5 个日志方法都必须走 LogFunc（LogFunc 在 Start 时生效）。
func TestSystemLogMethods(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(level LogLevel, format string, args ...interface{}) {
			mu.Lock()
			lines = append(lines, level.String()+" "+fmt.Sprintf(format, args...))
			mu.Unlock()
		}
		sc.TickInterval = 0
	})
	system.RegisterActorType(ActorTypeStart+1, func() Actor { return func(EnvelopeContext) {} })
	system.Start()
	defer system.Stop()
	system.LogDebug("d")
	system.LogInfo("i")
	system.LogWarn("w")
	system.LogError("e")
	system.LogFatal("f")
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 5 {
		t.Fatalf("expected 5 log lines, got %v", lines)
	}
}

// TickInterval <= 0：不创建 ticker，Stop 时不得 panic（ticker == nil 分支）。
func TestStopWithoutTicker(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 0
	})
	system.RegisterActorType(ActorTypeStart+1, func() Actor { return func(EnvelopeContext) {} })
	system.Start()
	system.Stop()
	if system.IsRunning() {
		t.Fatal("system should not be running after Stop")
	}
}

// 停机后本地投递 Notify / BatchSend 也必须报错（不得静默丢弃）。
func TestLocalRouterAfterStopDropsNotifyAndBatch(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	registerEchoActor(system, ActorTypeStart+1, 0)
	system.Start()
	system.Stop()

	ref := system.CreateActorRef(ActorTypeStart+1, "a")
	if err := system.LocalRouter(&EnvelopeNotify{
		ToActorRefs: []ActorRef{ref},
		NotifyType:  NotifyTypeWatch,
		Message:     &MsgOnWatchMsg{},
	}); err == nil {
		t.Fatal("Notify after Stop should return an error")
	}
	if err := system.LocalRouter(&EnvelopeBatchSend{
		ToActorRefs: []ActorRef{ref},
		Messages:    []interface{}{"x"},
	}); err == nil {
		t.Fatal("BatchSend after Stop should return an error")
	}
}

// Start 之前允许替换寻址函数，替换必须立即生效。
func TestSetCreateActorRefExFuncBeforeStart(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
	})
	defer system.Stop()
	system.SetCreateActorRefExFunc(func(systemId SystemId, actorType ActorType, actorId ActorId) ActorRef {
		return &ActorRefImpl{SystemId: systemId, ActorType: actorType, ActorId: actorId, GroupSlot: 7}
	})
	ref := system.CreateActorRef(ActorTypeStart+1, "x")
	if ref.GetGroupSlot() != 7 {
		t.Fatalf("custom CreateActorRefExFunc not applied: %v", ref.GetGroupSlot())
	}
}

// 同一 actorType 重复注册：走 Warn 分支且最后一次注册生效。
// 注意：Start 之前的日志走默认 logFunc（LogFunc 在 Start 时才装载），
// 因此这里断言行为而非日志内容。
func TestRegisterActorTypeDuplicateOverrides(t *testing.T) {
	system := NewSystem(func(sc *SystemConfig) {
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
		sc.TickInterval = 10 * time.Millisecond
	})
	defer system.Stop()
	tp := ActorTypeStart + 1
	var used int32
	system.RegisterActorType(tp, func() Actor {
		return func(ctx EnvelopeContext) {
			if _, ok := ctx.GetMessage().(string); ok {
				atomic.StoreInt32(&used, 1)
			}
		}
	})
	system.RegisterActorType(tp, func() Actor {
		return func(ctx EnvelopeContext) {
			if _, ok := ctx.GetMessage().(string); ok {
				atomic.StoreInt32(&used, 2)
			}
		}
	})
	system.Start()
	system.Send(system.CreateActorRef(tp, "a"), "x")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&used) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&used); got != 2 {
		t.Fatalf("last registration should win, used = %d", got)
	}
}
