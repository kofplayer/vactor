package vactor

import (
	"sync"
)

type Queue[T any] struct {
	buffer   *RingBuffer[T]
	mutex    sync.Mutex
	notEmpty *sync.Cond
	closed   bool
	zero     T
	// maxDepth 队列深度上限。>0 时达到上限的入队被拒绝（返回 false）；
	// 0 表示不限制（默认，保持旧行为）。
	// 用途：外部 watch / 事件队列由调用方创建，若消费者停止读取，无界队列会
	// 一直增长直至 OOM——设界后框架才能感知并摘除该订阅。
	maxDepth int
}

func NewQueue[T any]() *Queue[T] {
	ch := &Queue[T]{
		buffer: NewRingBuffer[T](16),
		closed: false,
	}
	ch.notEmpty = sync.NewCond(&ch.mutex)
	return ch
}

// SetMaxDepth 设置队列深度上限，0 表示不限制。
func (ch *Queue[T]) SetMaxDepth(maxDepth int) {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()
	ch.maxDepth = maxDepth
}

// MaxDepth 返回队列深度上限，0 表示不限制。
func (ch *Queue[T]) MaxDepth() int {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()
	return ch.maxDepth
}

func (ch *Queue[T]) Enqueue(value T) bool {
	ch.mutex.Lock()

	if ch.closed {
		ch.mutex.Unlock()
		return false
	}
	if ch.maxDepth > 0 && ch.buffer.Count() >= ch.maxDepth {
		ch.mutex.Unlock()
		return false
	}

	ch.buffer.Push(value)
	ch.mutex.Unlock()
	ch.notEmpty.Signal()
	return true
}

func (ch *Queue[T]) EnqueueBatch(values []T) bool {
	ch.mutex.Lock()

	if ch.closed {
		ch.mutex.Unlock()
		return false
	}
	// 整批接受或整批拒绝：部分入队会让调用方无从判断哪些到达了
	if ch.maxDepth > 0 && ch.buffer.Count()+len(values) > ch.maxDepth {
		ch.mutex.Unlock()
		return false
	}
	ch.buffer.PushBatch(values)
	ch.mutex.Unlock()
	l := len(values)
	if l > 1 {
		ch.notEmpty.Broadcast()
	} else if l > 0 {
		ch.notEmpty.Signal()
	}
	return true
}

func (ch *Queue[T]) Dequeue() (T, bool) {
	ch.mutex.Lock()

	for ch.buffer.Count() == 0 && !ch.closed {
		ch.notEmpty.Wait()
	}

	if ch.buffer.Count() == 0 && ch.closed {
		ch.mutex.Unlock()
		return ch.zero, false
	}

	value, _ := ch.buffer.Pop()
	ch.mutex.Unlock()
	return value, true
}

func (ch *Queue[T]) DequeueAll() ([]T, bool) {
	ch.mutex.Lock()
	for ch.buffer.Count() == 0 && !ch.closed {
		ch.notEmpty.Wait()
	}
	if ch.buffer.Count() == 0 && ch.closed {
		ch.mutex.Unlock()
		return nil, false
	}
	result := ch.buffer.PopAll()
	ch.mutex.Unlock()
	return result, true
}

// dequeueAllInto 与 DequeueAll 语义相同，但复用调用方提供的切片。
// 返回的切片只在调用方处理完这一批之前有效——mailbox 消费循环是唯一的使用者，
// 它天然满足"处理完上一批才取下一批"。公开的 DequeueAll 保持每次返回新切片，
// 避免外部调用方沿用返回结果时被静默改写。
func (ch *Queue[T]) dequeueAllInto(dst []T) ([]T, bool) {
	ch.mutex.Lock()
	for ch.buffer.Count() == 0 && !ch.closed {
		ch.notEmpty.Wait()
	}
	if ch.buffer.Count() == 0 && ch.closed {
		ch.mutex.Unlock()
		return dst[:0], false
	}
	result := ch.buffer.popAllInto(dst)
	ch.mutex.Unlock()
	return result, true
}

func (ch *Queue[T]) TryDequeue() (T, bool) {
	ch.mutex.Lock()

	if ch.buffer.Count() == 0 {
		ch.mutex.Unlock()
		return ch.zero, false
	}

	value, _ := ch.buffer.Pop()
	ch.mutex.Unlock()
	return value, true
}

func (ch *Queue[T]) TryDequeueAll() ([]T, bool) {
	ch.mutex.Lock()

	n := ch.buffer.Count()
	if n == 0 {
		if ch.closed {
			ch.mutex.Unlock()
			return nil, false
		}
		ch.mutex.Unlock()
		return nil, true
	}
	result := ch.buffer.PopAll()
	ch.mutex.Unlock()
	return result, true
}

// tryDequeueAllInto 是 TryDequeueAll 的复用版本（非阻塞），复用契约与 dequeueAllInto 相同。
func (ch *Queue[T]) tryDequeueAllInto(dst []T) ([]T, bool) {
	ch.mutex.Lock()

	if ch.buffer.Count() == 0 {
		if ch.closed {
			ch.mutex.Unlock()
			return dst[:0], false
		}
		ch.mutex.Unlock()
		return dst[:0], true
	}
	result := ch.buffer.popAllInto(dst)
	ch.mutex.Unlock()
	return result, true
}

func (ch *Queue[T]) Close() {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if !ch.closed {
		ch.closed = true
		ch.notEmpty.Broadcast()
	}
}

func (ch *Queue[T]) Len() int {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()
	return ch.buffer.Count()
}

func (ch *Queue[T]) IsClosed() bool {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()
	return ch.closed
}
