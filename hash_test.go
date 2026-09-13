package vactor_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

// HashActorId 的具体取值属于**跨版本行为契约**：dvactor 依赖同一哈希做跨节点
// 放置，改动它会让同一 actor 在升级前后落到不同节点。此用例锁定已知值。
func TestHashActorIdStableValues(t *testing.T) {
	cases := []struct {
		id   string
		want uint32
	}{
		{"", 2166136261},
		{"a", 3826002220},
		{"user1", 2692558073},
		{"room-1-player-2", 2610074109},
		{"same-id", 143740591},
	}
	for _, c := range cases {
		if got := vactor.HashActorId(vactor.ActorId(c.id)); got != c.want {
			t.Errorf("HashActorId(%q) = %d, want %d", c.id, got, c.want)
		}
	}
}

// 分布质量：结构化 id（user123、room-1-player-2）是业务常态，必须铺开。
// 历史上用的交替 XOR 会把 2 万个 id 压进约 760 个槽位，最重桶达最轻桶的 2.5 倍。
func TestHashActorIdDistributionIsUniform(t *testing.T) {
	const (
		n          = 20000
		groupCount = 16
	)
	var nums, users, rooms []vactor.ActorId
	for i := 1; i <= n; i++ {
		nums = append(nums, vactor.ActorId(fmt.Sprint(i)))
		users = append(users, vactor.ActorId(fmt.Sprintf("user%d", i)))
		rooms = append(rooms, vactor.ActorId(fmt.Sprintf("room-%d-player-%d", i/8, i%8)))
	}
	sets := []struct {
		name string
		ids  []vactor.ActorId
	}{
		{"纯数字", nums},
		{"user+数字", users},
		{"room-N-player-M", rooms},
	}

	for _, s := range sets {
		buckets := make([]int, groupCount)
		slots := map[vactor.GroupSlot]bool{}
		for _, id := range s.ids {
			slot := slotOf(id)
			buckets[(int(slot)-1)%groupCount]++
			slots[slot] = true
		}
		avg := float64(n) / groupCount
		min, max := n, 0
		var sum float64
		for _, c := range buckets {
			if c < min {
				min = c
			}
			if c > max {
				max = c
			}
			d := float64(c) - avg
			sum += d * d
		}
		cv := math.Sqrt(sum/float64(groupCount)) / avg
		if cv > 0.05 {
			t.Errorf("%s: 落组不均，变异系数 %.4f > 0.05（min=%d max=%d avg=%.1f）", s.name, cv, min, max, avg)
		}
		if float64(max) > 1.25*avg {
			t.Errorf("%s: 最重桶 %d 超过平均的 1.25 倍（avg=%.1f）", s.name, max, avg)
		}
		if len(slots) < n/2 {
			t.Errorf("%s: 唯一槽位仅 %d/%d，哈希区分度不足", s.name, len(slots), n)
		}
	}
}

// slotOf 是 GroupSlot 的规范推导：HashActorId 的低 16 位，0 归一到 1。
func slotOf(id vactor.ActorId) vactor.GroupSlot {
	slot := vactor.GroupSlot(vactor.HashActorId(id) & 0xFFFF)
	if slot == 0 {
		slot = 1
	}
	return slot
}

// CreateActorRef 的 GroupSlot 必须来自 HashActorId 的低 16 位。
// 若两者脱钩（例如某人只改了一处实现），本用例会立刻发现。
func TestCreateActorRefGroupSlotDerivesFromHashActorId(t *testing.T) {
	ts := testutil.NewSystem(t, nil)
	for _, id := range []vactor.ActorId{"", "a", "user1", "room-1-player-2", "纯数字"} {
		want := slotOf(id)
		if got := ts.CreateActorRef(100, id).GetGroupSlot(); got != want {
			t.Errorf("CreateActorRef(%q).GroupSlot = %d, want %d", id, got, want)
		}
	}
}

// "GroupSlot 永远不为 0"是一条不变式：哈希低 16 位为 0 时必须归一到 1。
// 该分支可达（期望约 6.5 万次尝试命中一次），所以动态搜出一个这样的 id 来验证，
// 而不是把它当成不可达分支放着不管。
func TestGroupSlotNeverZeroEvenWhenHashIsZero(t *testing.T) {
	var found vactor.ActorId
	for i := 0; i < 1_000_000; i++ {
		id := vactor.ActorId(fmt.Sprintf("zero-%d", i))
		if vactor.HashActorId(id)&0xFFFF == 0 {
			found = id
			break
		}
	}
	if found == "" {
		t.Skip("1e6 次尝试内未找到低 16 位为 0 的 ActorId（哈希可能已变）")
	}
	ts := testutil.NewSystem(t, nil)
	if got := ts.CreateActorRef(100, found).GetGroupSlot(); got != 1 {
		t.Fatalf("低 16 位为 0 的 id %q 的 GroupSlot 应归一为 1，实际 %d", found, got)
	}
}
