package vactor_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
)

// 停机必须把 goroutine 全部放掉：group goroutine、每个激活过的 actor goroutine、
// ticker 循环。任何一类漏掉都会在长期运行的进程里稳定累积（本轮修掉了集群侧同类问题，
// 这里守住单机侧）。
//
// 用"多轮创建/停止后的增量"而不是绝对数：同进程内其他（Parallel）用例也有 goroutine。
func TestStopReleasesAllGoroutines(t *testing.T) {
	settledCount := func() int {
		var last, same int
		for i := 0; i < 40; i++ {
			time.Sleep(50 * time.Millisecond)
			n := runtime.NumGoroutine()
			if n == last {
				if same++; same >= 3 {
					return n
				}
			} else {
				same = 0
			}
			last = n
		}
		return last
	}

	newSystem := func() vactor.System {
		return vactor.NewSystem(func(sc *vactor.SystemConfig) {
			sc.GroupCount = 4
			sc.TickInterval = 10 * time.Millisecond
			sc.DefaultStopInterval = 20 * time.Millisecond
			sc.LogFunc = func(vactor.LogLevel, string, ...interface{}) {}
		})
	}
	churn := func(s vactor.System, seed int) {
		for i := 0; i < 200; i++ {
			id := vactor.ActorId(rune('a'+i%26)) + vactor.ActorId(rune('A'+(i+seed)%26)) +
				vactor.ActorId(rune('0'+i%10))
			s.Send(s.CreateActorRef(100, id), "x")
		}
	}

	// 预热一轮（首次会拉起惰性运行时 goroutine），再取基线
	w := newSystem()
	w.RegisterActorType(100, func() vactor.Actor { return func(ctx vactor.EnvelopeContext) {} })
	w.Start()
	churn(w, 0)
	time.Sleep(100 * time.Millisecond)
	w.Stop()
	base := settledCount()

	// 轮数要明显大于容差：泄漏是"每轮一个"，轮数太少时增量会落在噪声里，
	// 反而测不出泄漏（第一版 5 轮 + 容差 5 就是这么漏掉 ticker goroutine 的）。
	const rounds = 12
	for r := 0; r < rounds; r++ {
		s := newSystem()
		s.RegisterActorType(100, func() vactor.Actor { return func(ctx vactor.EnvelopeContext) {} })
		s.Start()
		churn(s, r)
		// 让一部分 actor 因闲置被回收，覆盖回收路径上的 goroutine 退出
		time.Sleep(120 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.StopWithContext(ctx)
		cancel()
		if err != nil {
			t.Fatalf("round %d: StopWithContext: %v", r, err)
		}
	}

	if got := settledCount(); got > base+3 {
		t.Fatalf("goroutines leaked across %d start/stop rounds: baseline %d, now %d", rounds, base, got)
	}
}
