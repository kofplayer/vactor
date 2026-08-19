package vactor

import (
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
