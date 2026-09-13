package vactor_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kofplayer/vactor"
	"github.com/kofplayer/vactor/testutil"
)

// TestQueue 覆盖 Queue 的全部行为语义（阻塞/批量/关闭/ Try 系列）。
func TestQueue(t *testing.T) {
	t.Parallel()

	t.Run("FIFO", func(t *testing.T) {
		q := vactor.NewQueue[int]()
		for i := 0; i < 3; i++ {
			if !q.Enqueue(i) {
				t.Fatalf("enqueue %d failed", i)
			}
		}
		for i := 0; i < 3; i++ {
			v, ok := q.Dequeue()
			if !ok || v != i {
				t.Fatalf("dequeue got (%v,%v), want (%d,true)", v, ok, i)
			}
		}
	})

	t.Run("BatchOps", func(t *testing.T) {
		q := vactor.NewQueue[int]()
		if !q.EnqueueBatch([]int{1, 2, 3, 4}) {
			t.Fatal("enqueue batch failed")
		}
		msgs, ok := q.DequeueAll()
		if !ok || len(msgs) != 4 {
			t.Fatalf("dequeue all got (%v,%v)", msgs, ok)
		}
		for i, v := range msgs {
			if v != i+1 {
				t.Fatalf("batch order broken at %d: %v", i, msgs)
			}
		}
	})

	t.Run("TryDequeueEmpty", func(t *testing.T) {
		q := vactor.NewQueue[int]()
		if _, ok := q.TryDequeue(); ok {
			t.Fatal("try dequeue on empty queue should fail")
		}
		if msgs, ok := q.TryDequeueAll(); !ok || msgs != nil {
			t.Fatalf("try dequeue all on empty (not closed) queue should be (nil,true), got (%v,%v)", msgs, ok)
		}
		q.Enqueue(1)
		if v, ok := q.TryDequeue(); !ok || v != 1 {
			t.Fatalf("try dequeue got (%v,%v)", v, ok)
		}
	})

	t.Run("BlocksUntilEnqueue", func(t *testing.T) {
		q := vactor.NewQueue[int]()
		done := make(chan int, 1)
		go func() {
			v, _ := q.Dequeue()
			done <- v
		}()
		time.Sleep(50 * time.Millisecond)
		q.Enqueue(42)
		if v := testutil.WaitChan(t, done, 2*time.Second, "blocked dequeue"); v != 42 {
			t.Fatalf("got %v", v)
		}
	})

	t.Run("CloseUnblocksDequeueAll", func(t *testing.T) {
		q := vactor.NewQueue[int]()
		done := make(chan bool, 1)
		go func() {
			_, ok := q.DequeueAll()
			done <- ok
		}()
		time.Sleep(50 * time.Millisecond)
		q.Close()
		if ok := testutil.WaitChan(t, done, 2*time.Second, "close unblocks dequeue all"); ok {
			t.Fatal("dequeue all on closed empty queue should return ok=false")
		}
	})

	t.Run("DrainBeforeCloseReported", func(t *testing.T) {
		// 关闭前已入队的数据仍可取尽，取尽后 DequeueAll 才报告关闭
		q := vactor.NewQueue[int]()
		q.Enqueue(1)
		q.Enqueue(2)
		q.Close()
		msgs, ok := q.DequeueAll()
		if !ok || len(msgs) != 2 {
			t.Fatalf("drain after close: got (%v,%v), want 2 items", msgs, ok)
		}
		if _, ok := q.DequeueAll(); ok {
			t.Fatal("second dequeue all after drain should return ok=false")
		}
	})

	t.Run("EnqueueAfterClose", func(t *testing.T) {
		q := vactor.NewQueue[string]()
		q.Close()
		if q.Enqueue("x") {
			t.Fatal("enqueue after close should return false")
		}
		if !q.IsClosed() {
			t.Fatal("IsClosed should be true")
		}
		q.Close() // 幂等
		if _, ok := q.Dequeue(); ok {
			t.Fatal("dequeue on closed empty queue should fail")
		}
	})

	t.Run("Len", func(t *testing.T) {
		q := vactor.NewQueue[int]()
		if q.Len() != 0 {
			t.Fatal("new queue len should be 0")
		}
		q.EnqueueBatch([]int{1, 2, 3})
		if q.Len() != 3 {
			t.Fatalf("len = %d, want 3", q.Len())
		}
	})
}

// TestQueueConcurrentConservation 并发生产/消费总量守恒：
// 4 个生产者各发 500 条，关闭后消费者收到恰好 2000 条。
func TestQueueConcurrentConservation(t *testing.T) {
	t.Parallel()

	q := vactor.NewQueue[int]()
	const producers, perProducer = 4, 500
	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				if !q.Enqueue(p*perProducer + i) {
					t.Error("enqueue failed before close")
					return
				}
			}
		}(p)
	}
	go func() {
		wg.Wait()
		q.Close()
	}()

	var total atomic.Int64
	var consumed sync.WaitGroup
	consumed.Add(2)
	for c := 0; c < 2; c++ {
		go func() {
			defer consumed.Done()
			for {
				msgs, ok := q.DequeueAll()
				total.Add(int64(len(msgs)))
				if !ok {
					return
				}
			}
		}()
	}
	consumed.Wait()
	if got := total.Load(); got != producers*perProducer {
		t.Fatalf("total consumed = %d, want %d", got, producers*perProducer)
	}
}

// Queue 关闭语义：Enqueue/EnqueueBatch 返回 false；DequeueAll 取完存量后返回 !ok。
func TestQueueClosedSemantics(t *testing.T) {
	q := vactor.NewQueue[int]()
	if !q.EnqueueBatch([]int{1}) { // 单元素 -> Signal 分支
		t.Fatal("EnqueueBatch single should succeed")
	}
	if !q.EnqueueBatch([]int{2, 3}) { // 多元素 -> Broadcast 分支
		t.Fatal("EnqueueBatch multi should succeed")
	}
	q.Close()

	if !q.IsClosed() {
		t.Fatal("queue should report closed")
	}
	if q.Enqueue(4) {
		t.Fatal("Enqueue on closed queue must return false")
	}
	if q.EnqueueBatch([]int{5}) {
		t.Fatal("EnqueueBatch on closed queue must return false")
	}
	// 关闭后仍可取出存量数据
	msgs, ok := q.DequeueAll()
	if !ok || len(msgs) != 3 {
		t.Fatalf("DequeueAll after close = (%v,%v), want 3 items", msgs, ok)
	}
	if _, ok := q.DequeueAll(); ok {
		t.Fatal("DequeueAll on drained+closed queue must return ok=false")
	}
	if _, ok := q.Dequeue(); ok {
		t.Fatal("Dequeue on drained+closed queue must return ok=false")
	}
}

// TryDequeueAll 的三条分支：空且未关闭、有数据、空且已关闭。
func TestQueueTryDequeueAllBranches(t *testing.T) {
	q := vactor.NewQueue[int]()
	if msgs, ok := q.TryDequeueAll(); !ok || msgs != nil {
		t.Fatalf("empty+open = (%v,%v), want (nil,true)", msgs, ok)
	}
	q.Enqueue(1)
	q.Enqueue(2)
	msgs, ok := q.TryDequeueAll()
	if !ok || len(msgs) != 2 {
		t.Fatalf("TryDequeueAll = (%v,%v), want 2 items", msgs, ok)
	}
	q.Close()
	if msgs, ok := q.TryDequeueAll(); ok || msgs != nil {
		t.Fatalf("empty+closed = (%v,%v), want (nil,false)", msgs, ok)
	}
}
