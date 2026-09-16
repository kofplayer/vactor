package vactor

type RingBuffer[T any] struct {
	buffer []T
	size   int
	head   int
	tail   int
	count  int
	zero   T
}

func NewRingBuffer[T any](size int) *RingBuffer[T] {
	if size <= 0 {
		panic("ring buffer size must be greater than 0")
	}
	return &RingBuffer[T]{
		buffer: make([]T, size),
		size:   size,
		head:   0,
		tail:   0,
		count:  0,
	}
}

func (rb *RingBuffer[T]) Push(value T) {
	if rb.IsFull() {
		rb.grow(1)
	}

	rb.buffer[rb.tail] = value
	rb.tail = (rb.tail + 1) % rb.size
	rb.count++
}

func (rb *RingBuffer[T]) PushBatch(values []T) {
	n := len(values)
	if n == 0 {
		return
	}
	if rb.size-rb.count < n {
		rb.grow(n)
	}
	for _, value := range values {
		rb.buffer[rb.tail] = value
		rb.tail = (rb.tail + 1) % rb.size
		rb.count++
	}
}

// grow 扩容到至少还能容纳 minFree 个元素，并把环形数据搬迁到新缓冲的头部。
// 仅在剩余容量不足时调用。搬迁分两种情况：
//   - head < tail：数据在 [head, tail) 上连续；
//   - 其余（含满载时 head == tail）：数据被回绕切成 [head, size) + [0, tail) 两段。
func (rb *RingBuffer[T]) grow(minFree int) {
	newSize := rb.size
	for newSize-rb.count < minFree {
		newSize *= 2
	}
	newBuffer := make([]T, newSize)
	if rb.head < rb.tail {
		copy(newBuffer, rb.buffer[rb.head:rb.tail])
	} else {
		n := copy(newBuffer, rb.buffer[rb.head:rb.size])
		copy(newBuffer[n:], rb.buffer[0:rb.tail])
	}
	rb.buffer = newBuffer
	rb.size = newSize
	rb.head = 0
	rb.tail = rb.count
}

func (rb *RingBuffer[T]) Pop() (T, bool) {
	if rb.IsEmpty() {
		return rb.zero, false
	}

	value := rb.buffer[rb.head]
	rb.buffer[rb.head] = rb.zero
	rb.head = (rb.head + 1) % rb.size
	rb.count--
	return value, true
}

func (rb *RingBuffer[T]) PopAll() []T {
	return rb.popAllInto(nil)
}

// popAllInto 与 PopAll 语义相同，但把结果写入调用方提供的切片，以复用其底层数组。
//
// 返回的切片与 dst 共享底层数组，因此**只在调用方不再需要上一批结果时**才可复用 dst：
// mailbox 的消费循环（取一批 → 处理完 → 再取下一批）正好满足这一条件。
// 稳态下不再为每一批分配新切片（tick 扇出场景实测省下每轮 2 万次小对象分配）。
//
// 复用时会清掉 dst 中超出本批长度的残留引用，避免它们持有的信封无法被 GC。
func (rb *RingBuffer[T]) popAllInto(dst []T) []T {
	if rb.IsEmpty() {
		rb.head = 0
		rb.tail = 0
		return dst[:0]
	}
	n := rb.count
	if cap(dst) < n {
		dst = make([]T, n)
	} else {
		for i := n; i < len(dst); i++ {
			dst[i] = rb.zero
		}
		dst = dst[:n]
	}
	for i := 0; i < n; i++ {
		index := (rb.head + i) % rb.size
		dst[i] = rb.buffer[index]
		rb.buffer[index] = rb.zero
	}
	rb.head = 0
	rb.tail = 0
	rb.count = 0
	return dst
}

func (rb *RingBuffer[T]) Peek() (T, bool) {
	if rb.IsEmpty() {
		return rb.zero, false
	}

	return rb.buffer[rb.head], true
}

func (rb *RingBuffer[T]) IsEmpty() bool {
	return rb.count == 0
}

func (rb *RingBuffer[T]) IsFull() bool {
	return rb.count == rb.size
}

func (rb *RingBuffer[T]) Size() int {
	return rb.size
}

func (rb *RingBuffer[T]) Count() int {
	return rb.count
}

func (rb *RingBuffer[T]) Clear() {
	for i := 0; i < rb.count; i++ {
		rb.buffer[(rb.head+i)%rb.size] = rb.zero
	}
	rb.head = 0
	rb.tail = 0
	rb.count = 0
}

func (rb *RingBuffer[T]) GetAll() []T {
	if rb.IsEmpty() {
		return []T{}
	}

	result := make([]T, rb.count)
	for i := 0; i < rb.count; i++ {
		index := (rb.head + i) % rb.size
		result[i] = rb.buffer[index]
	}

	return result
}
