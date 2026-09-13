package vactor_test

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
)

// vactor 的标准基准：`go test -run '^$' -bench . -benchtime 1x ./` 可跑通，CI 据此做冒烟。
//
// 注意：CI 机器性能波动大，这里只保证「能编译、能运行、无 panic」，不设性能阈值；
// 具体数字请在固定机器上人工对比（`go test -bench . -benchmem ./`）。
// examples/benchmark 是同一套压测的人工观察版（1 万 actor × 1 万消息）。

const (
	benchEchoType      = vactor.ActorTypeStart + 80
	benchInitiatorType = vactor.ActorTypeStart + 81
	benchSinkType      = vactor.ActorTypeStart + 82
)

// newBenchSystem 建一个关掉 tick 与闲置回收的系统，避免周期维护干扰测量。
// extra 在 Start 之前调用，用于登记各基准自己的 actor 类型。
func newBenchSystem(b *testing.B, extra ...func(vactor.System)) vactor.System {
	b.Helper()
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.LogFunc = func(vactor.LogLevel, string, ...interface{}) {}
		sc.TickInterval = 0
		sc.DefaultStopInterval = 0
	})
	// echo：收到 int 就回 n+1
	s.RegisterActorType(benchEchoType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			if n, ok := ctx.GetMessage().(int); ok {
				ctx.Response(n+1, nil)
			}
		}
	})
	for _, f := range extra {
		f(s)
	}
	s.Start()
	b.Cleanup(s.Stop)
	return s
}

// BenchmarkActorRequestSync 一次同步请求的完整往返：入队 → 目标处理 → 响应回传。
func BenchmarkActorRequestSync(b *testing.B) {
	s := newBenchSystem(b)
	ref := s.CreateActorRef(benchEchoType, "sync")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Request(ref, i, 5*time.Second); err != nil {
			b.Fatalf("request failed: %v", err)
		}
	}
}

// BenchmarkActorRequestAsync 一次异步请求的完整往返：actor 内发起，回调返回时该次迭代结束。
func BenchmarkActorRequestAsync(b *testing.B) {
	done := make(chan struct{}, 1)
	s := newBenchSystem(b, func(sys vactor.System) {
		sys.RegisterActorType(benchInitiatorType, func() vactor.Actor {
			return func(ctx vactor.EnvelopeContext) {
				if _, ok := ctx.GetMessage().(int); !ok {
					return
				}
				ctx.RequestAsync(sys.CreateActorRef(benchEchoType, "async"), 1, 5*time.Second,
					func(interface{}, vactor.VAError) { done <- struct{}{} })
			}
		})
	})
	initiator := s.CreateActorRef(benchInitiatorType, "init")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Send(initiator, i)
		<-done
	}
}

// BenchmarkActorSend 纯发送路径吞吐。每 8192 条阻塞等一次处理进度，避免 mailbox
// 无界积压——因此计时里含节流等待，衡量的是**可持久的发送吞吐**，而非瞬时入队速度。
func BenchmarkActorSend(b *testing.B) {
	var processed atomic.Int64
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.LogFunc = func(vactor.LogLevel, string, ...interface{}) {}
		sc.TickInterval = 0
		sc.DefaultStopInterval = 0
	})
	s.RegisterActorType(benchSinkType, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			if _, ok := ctx.GetMessage().(int); ok {
				processed.Add(1)
			}
		}
	})
	s.Start()
	defer s.Stop()
	ref := s.CreateActorRef(benchSinkType, "sink")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Send(ref, i)
		if (i+1)%8192 == 0 {
			for processed.Load() < int64(i+1) {
				runtime.Gosched()
			}
		}
	}
	b.StopTimer()
	for processed.Load() < int64(b.N) {
		runtime.Gosched()
	}
}
