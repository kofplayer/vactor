package vactor

import (
	"fmt"
	"testing"
)

// BenchmarkNotifyFanout 事件/watch 广播的扇出开销：一次 Notify 携带 N 个 watcher，
// 经 LocalRouter 按 group 拆分后投递。
//
// 用途：O3 的决策依据——把"notify 自身构造 watcher 切片"的 1 次分配，与路由扇出
// 的其余分配放在同一个数量级里对比，避免为了 1 次分配去引入复用风险。
//
// 注意：refs 在循环外构造，所以本基准**不含** actorContext.notify 里那次
// make([]ActorRef, len(watchers))；把两者相减即可得到它的占比。
func BenchmarkNotifyFanout(b *testing.B) {
	const (
		watchers = 1000
		groups   = 4
	)
	s := NewSystem(func(sc *SystemConfig) {
		sc.GroupCount = groups
		sc.LogFunc = func(LogLevel, string, ...interface{}) {}
	}).(*system)
	s.groupCount = groups
	s.actorGroups = make([]*actorGroup, groups)
	for i := range s.actorGroups {
		s.actorGroups[i] = newActorGroup(s)
	}
	s.systemId = 0

	refs := make([]ActorRef, watchers)
	for i := range refs {
		refs[i] = &ActorRefImpl{
			SystemId:  0,
			GroupSlot: GroupSlot(i%groups + 1),
			ActorType: ActorTypeStart + 1,
			ActorId:   ActorId(fmt.Sprint(i)),
		}
	}
	msg := &MsgOnWatchMsg{WatchType: 1}
	bufs := make([][]Envelope, groups)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.LocalRouter(&EnvelopeNotify{
			ToActorRefs: refs,
			NotifyType:  NotifyTypeWatch,
			Message:     msg,
		})
		// 及时清空 group mailbox，避免无界增长与随之而来的扩容分配
		for g := range s.actorGroups {
			bufs[g], _ = s.actorGroups[g].mailbox.tryDequeueAllInto(bufs[g])
		}
	}
}
