package vactor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

// StopWithContext 在停机被卡住时必须按时限返回，而不是永久挂住。
// 卡住的成因很常见：handler 里做一次阻塞调用，或 handler 用 Request(timeout<=0)
// 等一个不会来的响应——此时 Stop 的 wg.Wait() 永远回不来（上一轮检查里真实发生
// 过：整个测试包被拖到 go test 超时）。
func TestStopWithContextTimesOutOnStuckHandler(t *testing.T) {
	logs := testutil.NewLogBuffer()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }

	entered := make(chan struct{}, 1)
	exited := make(chan struct{}, 1)

	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.GroupCount = 1
		sc.TickInterval = 10 * time.Millisecond
		sc.LogFunc = logs.LogFunc
	})
	s.RegisterActorType(100, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			switch ctx.GetMessage().(type) {
			case *vactor.MsgOnStop:
				// 停机时的收尾回调同样会阻塞：不放行就永远退不出去
				<-release
				select {
				case exited <- struct{}{}:
				default:
				}
			case string:
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
			}
		}
	})
	s.Start()
	// 务必先放行再让清理阶段的 Stop 跑完，否则测试会挂死在 Cleanup 里
	t.Cleanup(func() {
		releaseHandler()
		s.Stop()
	})

	s.Send(s.CreateActorRef(100, "stuck"), "block")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never entered")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.StopWithContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopWithContext = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("StopWithContext blocked %v: the deadline was not honored", elapsed)
	}
	if !logs.Contains("did not finish in time") {
		t.Fatal("a timed-out stop must log a diagnostic with the still-running goroutine count")
	}

	// 放行后后台停机必须自己走完：不能留下退不出去的 goroutine
	releaseHandler()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("background shutdown did not complete after the handler was released")
	}
}

// 正常停机路径：立即返回 nil，且与 Stop 混用保持幂等。
func TestStopWithContextHappyPathAndIdempotence(t *testing.T) {
	var mu sync.Mutex
	stops := 0
	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.GroupCount = 2
		sc.TickInterval = 10 * time.Millisecond
		sc.LogFunc = func(vactor.LogLevel, string, ...interface{}) {}
	})
	s.RegisterActorType(100, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			if _, ok := ctx.GetMessage().(*vactor.MsgOnStop); ok {
				mu.Lock()
				stops++
				mu.Unlock()
			}
		}
	})
	s.Start()
	a := s.CreateActorRef(100, "a")
	s.Send(a, "x")
	time.Sleep(50 * time.Millisecond)

	err := s.StopWithContext(context.Background())
	if err != nil {
		t.Fatalf("clean stop must return nil, got %v", err)
	}
	mu.Lock()
	got := stops
	mu.Unlock()
	if got != 1 {
		t.Fatalf("MsgOnStop delivered %d times, want exactly 1", got)
	}
	// 重复 Stop / StopWithContext 都不报错、不 panic
	s.Stop()
	if err := s.StopWithContext(context.Background()); err != nil {
		t.Fatalf("second StopWithContext = %v, want nil (idempotent)", err)
	}
	// 已停止的系统再发消息必须明确报错，而不是无声接受
	if _, err := s.Request(a, "after-stop", time.Second); err == nil {
		t.Fatal("Request after Stop must fail")
	}
}
