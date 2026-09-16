package vactor

import "testing"

// 复用取批的跨批正确性：第二批比第一批短时，长度必须收缩，且不得看到上一批的残留。
func TestQueueDequeueAllIntoReuse(t *testing.T) {
	q := NewQueue[int]()
	var buf []int

	q.Enqueue(1)
	q.Enqueue(2)
	q.Enqueue(3)
	buf, ok := q.dequeueAllInto(buf)
	if !ok || len(buf) != 3 || buf[0] != 1 || buf[2] != 3 {
		t.Fatalf("batch1 = %v ok=%v, want [1 2 3]", buf, ok)
	}
	backing := &buf[0]

	// 第二批更短
	q.Enqueue(9)
	buf, ok = q.dequeueAllInto(buf)
	if !ok || len(buf) != 1 || buf[0] != 9 {
		t.Fatalf("batch2 = %v ok=%v, want [9]", buf, ok)
	}
	if &buf[0] != backing {
		t.Fatal("expected the same backing array to be reused across batches")
	}

	// 空队列：非阻塞版本必须立即返回空，而不是阻塞等待
	if got, ok := q.tryDequeueAllInto(buf); !ok || len(got) != 0 {
		t.Fatalf("empty tryDequeueAllInto = %v ok=%v, want empty/true", got, ok)
	}

	// 阻塞版本在队列为空且未关闭时会等待；关闭后必须立刻返回 !ok 让消费方退出
	q.Close()
	if got, ok := q.dequeueAllInto(buf); ok || len(got) != 0 {
		t.Fatalf("closed+empty dequeueAllInto = %v ok=%v, want empty/false", got, ok)
	}
}

// 复用时必须清掉超出本批长度的残留引用，否则上一批的信封会被底层数组一直持有、
// 无法被 GC（这正是 mailbox 复用 buffer 最容易踩的坑）。
func TestQueueDequeueAllIntoClearsStaleTail(t *testing.T) {
	q := NewQueue[*int]()
	var buf []*int
	a, b, c := 1, 2, 3
	for _, p := range []*int{&a, &b, &c} {
		q.Enqueue(p)
	}
	buf, _ = q.dequeueAllInto(buf)
	if len(buf) != 3 {
		t.Fatalf("batch1 len = %d, want 3", len(buf))
	}
	capacity := cap(buf)

	// 第二批只放一条：底层数组的 [1,3) 必须被清空
	q.Enqueue(&a)
	buf, _ = q.dequeueAllInto(buf)
	if len(buf) != 1 || buf[0] != &a {
		t.Fatalf("batch2 = %v, want [&a]", buf)
	}
	if cap(buf) != capacity {
		t.Fatalf("backing array capacity changed: %d -> %d", capacity, cap(buf))
	}
	tail := buf[:capacity]
	for i := 1; i < capacity; i++ {
		if tail[i] != nil {
			t.Fatalf("stale reference at index %d was not cleared: %v", i, tail[i])
		}
	}
}

// 公开的 DequeueAll / TryDequeueAll 必须继续返回**互相独立**的切片：
// 外部调用方可能沿用上一批的结果，若它们也改成复用就是静默改写用户数据。
func TestQueuePublicBatchAPIsReturnIndependentSlices(t *testing.T) {
	q := NewQueue[int]()
	q.Enqueue(1)
	q.Enqueue(2)
	first, ok := q.DequeueAll()
	if !ok {
		t.Fatal("first DequeueAll failed")
	}

	q.Enqueue(3)
	second, ok := q.TryDequeueAll()
	if !ok {
		t.Fatal("second TryDequeueAll failed")
	}

	if len(first) != 2 || first[0] != 1 || first[1] != 2 {
		t.Fatalf("previously returned slice was mutated: %v, want [1 2]", first)
	}
	if len(second) != 1 || second[0] != 3 {
		t.Fatalf("second = %v, want [3]", second)
	}
}
