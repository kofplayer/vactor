package vactor_test

import (
	"testing"

	"github.com/kofplayer/vactor"
)

// TestRingBuffer 覆盖环形缓冲的回绕、扩容与各操作语义。
func TestRingBuffer(t *testing.T) {
	t.Parallel()

	t.Run("Wraparound", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](4)
		// 制造回绕：填满、取走一半、再填满
		for i := 0; i < 4; i++ {
			rb.Push(i)
		}
		for i := 0; i < 2; i++ {
			if v, _ := rb.Pop(); v != i {
				t.Fatalf("pop %d got %d", i, v)
			}
		}
		for i := 4; i < 8; i++ {
			rb.Push(i)
		}
		for i := 2; i < 8; i++ {
			v, ok := rb.Pop()
			if !ok || v != i {
				t.Fatalf("pop got (%d,%v), want %d", v, ok, i)
			}
		}
		if !rb.IsEmpty() {
			t.Fatal("buffer should be empty")
		}
	})

	t.Run("Growth", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](4)
		for i := 0; i < 100; i++ {
			rb.Push(i)
		}
		if rb.Count() != 100 {
			t.Fatalf("count = %d", rb.Count())
		}
		for i := 0; i < 100; i++ {
			v, ok := rb.Pop()
			if !ok || v != i {
				t.Fatalf("after growth pop got (%d,%v), want %d", v, ok, i)
			}
		}
	})

	t.Run("PushBatchOverflow", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](2)
		values := make([]int, 10)
		for i := range values {
			values[i] = i
		}
		rb.PushBatch(values)
		if rb.Count() != 10 {
			t.Fatalf("count = %d", rb.Count())
		}
		for i := 0; i < 10; i++ {
			if v, _ := rb.Pop(); v != i {
				t.Fatalf("batch order broken at %d", i)
			}
		}
	})

	t.Run("PushBatchEmpty", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](4)
		rb.PushBatch(nil)
		if !rb.IsEmpty() {
			t.Fatal("empty batch should not change buffer")
		}
	})

	t.Run("PopAll", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](4)
		rb.PushBatch([]int{1, 2, 3})
		all := rb.PopAll()
		if len(all) != 3 || all[0] != 1 || all[2] != 3 {
			t.Fatalf("pop all = %v", all)
		}
		if !rb.IsEmpty() || rb.Count() != 0 {
			t.Fatal("pop all should reset buffer state")
		}
		if got := rb.PopAll(); len(got) != 0 {
			t.Fatalf("pop all on empty = %v", got)
		}
	})

	t.Run("Peek", func(t *testing.T) {
		rb := vactor.NewRingBuffer[string](4)
		if _, ok := rb.Peek(); ok {
			t.Fatal("peek on empty should fail")
		}
		rb.Push("a")
		v, ok := rb.Peek()
		if !ok || v != "a" {
			t.Fatalf("peek got (%v,%v)", v, ok)
		}
		if rb.Count() != 1 {
			t.Fatal("peek should not consume")
		}
	})

	t.Run("GetAllNonDestructive", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](8)
		rb.PushBatch([]int{1, 2, 3})
		first := rb.GetAll()
		second := rb.GetAll()
		if len(first) != 3 || len(second) != 3 {
			t.Fatal("get all length mismatch")
		}
		for i := range first {
			if first[i] != second[i] || first[i] != i+1 {
				t.Fatalf("get all values mismatch: %v vs %v", first, second)
			}
		}
		if rb.Count() != 3 {
			t.Fatal("get all should not consume")
		}
	})

	t.Run("Metrics", func(t *testing.T) {
		rb := vactor.NewRingBuffer[int](4)
		if rb.Size() != 4 || !rb.IsEmpty() || rb.IsFull() {
			t.Fatal("initial metrics wrong")
		}
		rb.PushBatch([]int{1, 2, 3, 4})
		if !rb.IsFull() || rb.Count() != 4 {
			t.Fatal("full metrics wrong")
		}
	})

	t.Run("Clear", func(t *testing.T) {
		rb := vactor.NewRingBuffer[*int](4)
		a, b := 1, 2
		rb.Push(&a)
		rb.Push(&b)
		rb.Clear()
		if !rb.IsEmpty() || rb.Count() != 0 {
			t.Fatal("clear should empty buffer")
		}
		rb.Push(&a)
		if v, _ := rb.Pop(); v != &a {
			t.Fatal("push after clear broken")
		}
	})
}

// TestNewRingBufferInvalidSize 非法容量必须 panic。
func TestNewRingBufferInvalidSize(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, -1, -100} {
		t.Run("size-"+func() string {
			if size == 0 {
				return "0"
			}
			return "negative"
		}(), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected panic for size %d", size)
				}
			}()
			vactor.NewRingBuffer[int](size)
		})
	}
}

// PushBatch 在环形回绕状态下的扩容（head < tail 的数据搬迁分支）。
func TestRingBufferPushBatchGrowWrapped(t *testing.T) {
	rb := vactor.NewRingBuffer[int](4)
	rb.PushBatch([]int{1, 2}) // head=0 tail=2 count=2，剩余空间 2
	rb.PushBatch([]int{3, 4, 5})
	if rb.Size() != 8 {
		t.Fatalf("size = %d, want 8", rb.Size())
	}
	if rb.Count() != 5 {
		t.Fatalf("count = %d, want 5", rb.Count())
	}
	all := rb.GetAll()
	want := []int{1, 2, 3, 4, 5}
	for i := range want {
		if i >= len(all) || all[i] != want[i] {
			t.Fatalf("GetAll = %v, want %v", all, want)
		}
	}
}

// 空缓冲的边界语义：Pop / GetAll / Peek 均安全返回零值。
func TestRingBufferEmptyEdges(t *testing.T) {
	rb := vactor.NewRingBuffer[int](4)
	if v, ok := rb.Pop(); ok || v != 0 {
		t.Fatalf("Pop on empty = (%v,%v)", v, ok)
	}
	if _, ok := rb.Peek(); ok {
		t.Fatal("Peek on empty should return ok=false")
	}
	if all := rb.GetAll(); len(all) != 0 {
		t.Fatalf("GetAll on empty = %v", all)
	}
	rb.Clear()
	if !rb.IsEmpty() || rb.Count() != 0 {
		t.Fatal("Clear on empty should be a no-op")
	}
}

// Push 在满载时的扩容路径（数据未被回绕覆盖）。
func TestRingBufferPushGrow(t *testing.T) {
	rb := vactor.NewRingBuffer[int](4)
	for i := 0; i < 4; i++ {
		rb.Push(i)
	}
	rb.Push(4) // 触发 ×2 扩容
	if rb.Size() != 8 || rb.Count() != 5 {
		t.Fatalf("size=%d count=%d, want 8/5", rb.Size(), rb.Count())
	}
	all := rb.GetAll()
	for i := 0; i < 5; i++ {
		if all[i] != i {
			t.Fatalf("GetAll = %v, want 0..4", all)
		}
	}
}
