package events

import "sync"

type Broker[T any] struct {
	mu   sync.RWMutex
	subs map[chan T]struct{}
}

func NewBroker[T any]() *Broker[T] {
	return &Broker[T]{subs: make(map[chan T]struct{})}
}

func (b *Broker[T]) Publish(event T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
			// Runtime state is eventually refreshed from the API. A slow SSE
			// consumer must never block a Tailcat engine callback.
		}
	}
}

func (b *Broker[T]) Subscribe(buffer int) (<-chan T, func()) {
	ch := make(chan T, max(1, buffer))
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, sync.OnceFunc(func() {
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	})
}
