package vactor

import (
	"fmt"
	"testing"
)

// BenchmarkTickFanout 对比 group 在一批 tick 上的扇出开销：
//   - tickEnabled：所有 actor 都需要 tick（旧行为）
//   - tickDisabled：所有 actor 都空闲且声明不需要 tick（优化后不再投递）
//
// 运行：go test -bench BenchmarkTickFanout -benchtime 200x ./vactor
func BenchmarkTickFanout(b *testing.B) {
	const actors = 20000
	for _, disabled := range []bool{false, true} {
		name := "tickEnabled"
		if disabled {
			name = "tickDisabled"
		}
		b.Run(name, func(b *testing.B) {
			s := NewSystem(func(sc *SystemConfig) {
				sc.TickInterval = 0 // 不启动 system ticker，由基准手动投递
				sc.DefaultStopInterval = 0
				sc.LogFunc = func(LogLevel, string, ...interface{}) {}
			}).(*system)
			s.actorCreators[ActorTypeStart+1] = func() Actor {
				return func(EnvelopeContext) {}
			}
			s.groupCount = 1
			s.actorGroups = []*actorGroup{newActorGroup(s)}
			s.systemId = 0
			g := s.actorGroups[0]
			for i := 0; i < actors; i++ {
				ref := &ActorRefImpl{ActorType: ActorTypeStart + 1, ActorId: ActorId(fmt.Sprint(i)), GroupSlot: 1}
				mailbox := NewQueue[Envelope]()
				ctx := newActorContext(g, ref, mailbox, nil)
				ctx.setTickEnabled(!disabled)
				g.actorContexts[*ref] = ctx
			}
			tick := &envelopeTick{}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				g.processBatch([]Envelope{tick})
				// 模拟 actor 及时消费，避免 mailbox 无界增长。
				// 必须与 actorContext 实际走的取批方式一致（复用同一 buffer）：
				// 若这里用公开的 TryDequeueAll（每次返回新切片），基准会把
				// **消费侧**的分配记到扇出头上——2 万 actor 就是每轮 2 万次分配。
				// 这里用非阻塞版本：actor 循环用阻塞版是因为它总是有下一批要等，
				// 而基准里 tickDisabled 时根本没有消息可等。
				for _, c := range g.actorContexts {
					c.batchBuf, _ = c.mailbox.tryDequeueAllInto(c.batchBuf)
				}
			}
		})
	}
}

// BenchmarkMailboxDrain 隔离 O1 的效果：actor 消费循环每取一批的分配次数。
//   - public：公开的 DequeueAll（每次 make 新切片，供外部调用方沿用返回值）
//   - reuse： actor 循环实际走的 dequeueAllInto（复用同一 buffer）
func BenchmarkMailboxDrain(b *testing.B) {
	const depth = 16
	for _, reuse := range []bool{false, true} {
		name := "public"
		if reuse {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			q := NewQueue[Envelope]()
			tick := &envelopeTick{}
			var buf []Envelope
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < depth; j++ {
					q.Enqueue(tick)
				}
				if reuse {
					var ok bool
					buf, ok = q.dequeueAllInto(buf)
					if !ok {
						b.Fatal("closed")
					}
				} else if _, ok := q.DequeueAll(); !ok {
					b.Fatal("closed")
				}
			}
		})
	}
}
