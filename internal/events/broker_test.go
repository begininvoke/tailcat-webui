package events

import "testing"

func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	broker := NewBroker[int]()
	stream, unsubscribe := broker.Subscribe(1)
	defer unsubscribe()
	broker.Publish(1)
	broker.Publish(2)
	if got := <-stream; got != 1 {
		t.Fatalf("first event = %d, want 1", got)
	}
}
