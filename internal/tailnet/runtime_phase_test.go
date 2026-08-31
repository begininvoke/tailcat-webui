package tailnet

import (
	"reflect"
	"sync"
	"testing"

	"github.com/ca-x/tailcat-webui/internal/events"
)

func TestRuntimePhasesAreExhaustive(t *testing.T) {
	got := []RuntimePhase{
		RuntimePhaseIdle,
		RuntimePhaseStarting,
		RuntimePhaseConnecting,
		RuntimePhaseReady,
		RuntimePhaseRunning,
		RuntimePhaseStopping,
		RuntimePhaseStopped,
		RuntimePhaseError,
		RuntimePhaseInterrupted,
	}
	want := []RuntimePhase{"idle", "starting", "connecting", "ready", "running", "stopping", "stopped", "error", "interrupted"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runtime phases = %v, want %v", got, want)
	}
	if got := events.RuntimePhase(RuntimePhaseReady); got != events.RuntimePhaseReady {
		t.Errorf("tailnet RuntimePhase alias = %q, want %q", got, events.RuntimePhaseReady)
	}
}

func TestSameOwnerRuntimeEventsFollowSequenceOrder(t *testing.T) {
	const publishers = 1_024
	manager := &Manager{
		userEvents:     make(map[string]*events.Broker[events.Envelope]),
		eventSequences: make(map[string]uint64),
	}
	stream, unsubscribe := manager.Events("owner-1").Subscribe(publishers)
	defer unsubscribe()

	start := make(chan struct{})
	var publishersWG sync.WaitGroup
	for range publishers {
		publishersWG.Go(func() {
			<-start
			manager.publish("owner-1", "client", "client-1", RuntimePhaseReady, "")
		})
	}
	close(start)
	publishersWG.Wait()

	for sequence := uint64(1); sequence <= publishers; sequence++ {
		if got := (<-stream).Sequence; got != sequence {
			t.Fatalf("event sequence = %d, want %d", got, sequence)
		}
	}
}
