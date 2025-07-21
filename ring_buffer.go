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
		newSize := rb.size * 2
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

	rb.buffer[rb.tail] = value
	rb.tail = (rb.tail + 1) % rb.size
	rb.count++
}

func (rb *RingBuffer[T]) PushBatch(values []T) {
	n := len(values)
	if n == 0 {
		return
	}
	for rb.size-rb.count < n {
		newSize := rb.size * 2
		newBuffer := make([]T, newSize)
		if rb.head < rb.tail {
			copy(newBuffer, rb.buffer[rb.head:rb.tail])
		} else {
			c := copy(newBuffer, rb.buffer[rb.head:rb.size])
			copy(newBuffer[c:], rb.buffer[0:rb.tail])
		}
		rb.buffer = newBuffer
		rb.size = newSize
		rb.head = 0
		rb.tail = rb.count
	}
	for _, value := range values {
		rb.buffer[rb.tail] = value
		rb.tail = (rb.tail + 1) % rb.size
		rb.count++
	}
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
	if rb.IsEmpty() {
		return []T{}
	}
	result := make([]T, rb.count)
	for i := 0; i < rb.count; i++ {
		index := (rb.head + i) % rb.size
		result[i] = rb.buffer[index]
		rb.buffer[index] = rb.zero
	}
	rb.head = 0
	rb.tail = 0
	rb.count = 0
	return result
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
