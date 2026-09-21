package vactor_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
)

// 特征测试（**记录已知未修的代价，不是断言这是好行为**）：
// 被请求方的 handler 收到 Request 后从不调用 Response 时，processingRequestCount 永久
// 留在 >0，该 actor 再也不会被闲置回收，它的 mailbox 也一起常驻 group。
//
// 保留这个用例是为了让"目前没有回收机制"在代码里可见：本轮曾实现过按 TTL 放行回收
// （含逐条计时），复查后撤销，回收策略待重新设计。方案定了之后请改写或删除本用例。
// 已知约束：不要用"共享一个挂起起点"来判定超时——后到的请求会被最老那条的时刻判过期，
// 一条漏应答的请求会压掉其它挂起请求的保护窗口。
//
// A/B 对照：同类型、同投递节奏，唯一差别是"是否欠过一次应答"。
func TestUnansweredRequestPinsActorKnownLimitation(t *testing.T) {
	const stopInterval = 100 * time.Millisecond

	var mu sync.Mutex
	starts := map[vactor.ActorId]int{}
	bump := func(id vactor.ActorId) {
		mu.Lock()
		starts[id]++
		mu.Unlock()
	}
	countOf := func(id vactor.ActorId) int {
		mu.Lock()
		defer mu.Unlock()
		return starts[id]
	}

	s := vactor.NewSystem(func(sc *vactor.SystemConfig) {
		sc.GroupCount = 1
		sc.TickInterval = 10 * time.Millisecond
		sc.DefaultStopInterval = stopInterval
		sc.LogFunc = func(vactor.LogLevel, string, ...interface{}) {}
	})
	s.RegisterActorType(100, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			if _, ok := ctx.GetMessage().(*vactor.MsgOnStart); ok {
				bump(ctx.GetActorRef().GetActorId())
			}
			// 只有 control 应答；leaky 收到请求后什么都不回 = 缺陷现场
			if ctx.GetActorRef().GetActorId() == "control" && ctx.GetFromActorRef() != nil {
				ctx.Response("ok", nil)
			}
		}
	})
	s.RegisterActorType(101, func() vactor.Actor {
		return func(ctx vactor.EnvelopeContext) {
			m, ok := ctx.GetMessage().(string)
			if !ok {
				return
			}
			id := vactor.ActorId("leaky")
			if m == "control" {
				id = "control"
			}
			if _, err := ctx.Request(ctx.CreateActorRef(100, id), "ping", 50*time.Millisecond); err != nil && id == "control" {
				t.Errorf("control request should have been answered, got %v", err)
			}
		}
	})
	s.Start()
	defer s.Stop()

	// 各制造一次请求：leaky 被欠下应答，control 应答后计数归零
	s.Send(s.CreateActorRef(101, "caller"), "go")
	s.Send(s.CreateActorRef(101, "caller2"), "control")
	time.Sleep(2 * stopInterval)

	restarts := func(id vactor.ActorId, rounds int) int {
		base := countOf(id)
		for i := 0; i < rounds; i++ {
			s.Send(s.CreateActorRef(100, id), "x")
			time.Sleep(3 * stopInterval)
		}
		return countOf(id) - base
	}

	// 对照组先证明"这套节奏下正常 actor 确实会被回收"，否则后面的断言毫无意义
	if ctrl := restarts("control", 6); ctrl == 0 {
		t.Fatal("control actor was never recycled: the harness cannot distinguish pinning from a broken test")
	}
	if got := restarts("leaky", 6); got != 0 {
		t.Fatalf("行为已改变：leaky actor 被重建 %d 次。若已引入未应答请求的回收策略，请改写或删除本用例", got)
	}
}
