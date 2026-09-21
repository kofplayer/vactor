package vactor

import "testing"

// O5 的判定口径：group.processEnvelope 每条消息要做 2 次 map[ActorRefImpl] 查找
// （mailbox + context），每次都要对 ActorId 字符串做哈希；mailbox 与 context 分表存放
// 是历史结构。这里直接驱动该函数，绕开 goroutine 唤醒，所以能分辨出十纳秒级的差值。
func BenchmarkProcessEnvelopeRouting(b *testing.B) {
	for _, idLen := range []int{4, 16} {
		b.Run(actorIdOfLen(idLen), func(b *testing.B) {
			s := NewSystem(func(sc *SystemConfig) {
				sc.LogFunc = func(LogLevel, string, ...interface{}) {}
			}).(*system)
			s.actorGroups = []*actorGroup{newActorGroup(s)}
			s.groupCount = 1
			s.actorCreators[ActorTypeStart+1] = func() Actor {
				return func(EnvelopeContext) {}
			}
			ref := &ActorRefImpl{ActorType: ActorTypeStart + 1, ActorId: ActorId(actorIdOfLen(idLen)), GroupSlot: 1}
			g := s.actorGroups[0]
			env := &EnvelopeSend{ToActorRef: ref, Message: "m"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				g.processEnvelope(ref, env)
			}
		})
	}
}

func actorIdOfLen(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	return string(buf)
}
