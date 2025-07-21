package vactor

import (
	"sync"
)

type Queue[T any] struct {
	buffer      *RingBuffer[T]
	mutex       sync.Mutex
	notEmpty    *sync.Cond
	notifyClose chan struct{}
	closed      bool
	zero        T
}

func NewQueue[T any]() *Queue[T] {
	ch := &Queue[T]{
		buffer:      NewRingBuffer[T](16),
		notifyClose: make(chan struct{}),
		closed:      false,
	}
	ch.notEmpty = sync.NewCond(&ch.mutex)
	return ch
}

func (ch *Queue[T]) Enqueue(value T) bool {
	ch.mutex.Lock()

	if ch.closed {
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

func (ch *Queue[T]) Close() {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if !ch.closed {
		ch.closed = true
		close(ch.notifyClose)
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
