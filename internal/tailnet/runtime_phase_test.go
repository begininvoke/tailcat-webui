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

func TestDiagnosticEventsShareOwnerRuntimeSequence(t *testing.T) {
	manager := &Manager{
		userEvents:     make(map[string]*events.Broker[events.Envelope]),
		eventSequences: make(map[string]uint64),
	}
	stream, unsubscribe := manager.Events("owner-1").Subscribe(3)
	defer unsubscribe()
	manager.publish("owner-1", "client", "client-1", RuntimePhaseReady, "")
	payload := map[string]any{"client_id": "client-1", "kind": "ping", "status": "running", "progress": 40}
	manager.PublishEvent("owner-1", events.Envelope{Type: "diagnostic", ResourceKind: "diagnostic", ResourceID: "run-1", OperationID: "run-1", Phase: RuntimePhaseRunning, Payload: payload})
	transferPayload := map[string]any{"job_id": "job-1", "status": "running"}
	manager.PublishEvent("owner-1", events.Envelope{Type: "transfer", ResourceKind: "transfer", ResourceID: "job-1", OperationID: "job-1", Phase: RuntimePhaseRunning, Payload: transferPayload})

	first, second, third := <-stream, <-stream, <-stream
	if first.Sequence != 1 || second.Sequence != 2 || third.Sequence != 3 {
		t.Fatalf("event sequences = %d, %d, %d", first.Sequence, second.Sequence, third.Sequence)
	}
	if second.Type != "diagnostic" || second.ResourceKind != "diagnostic" || second.ResourceID != "run-1" || second.OperationID != "run-1" || second.Phase != RuntimePhaseRunning || !reflect.DeepEqual(second.Payload, payload) {
		t.Fatalf("diagnostic event = %+v", second)
	}
	if third.Version != 1 || third.Type != "transfer" || third.ResourceID != "job-1" || !reflect.DeepEqual(third.Payload, transferPayload) {
		t.Fatalf("transfer event = %+v", third)
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

func TestOwnerEventSequenceSurvivesBrokerRelease(t *testing.T) {
	manager := &Manager{
		userEvents:     make(map[string]*events.Broker[events.Envelope]),
		eventSequences: make(map[string]uint64),
	}
	firstStream, unsubscribeFirst := manager.Events("owner-1").Subscribe(1)
	manager.PublishEvent("owner-1", events.Envelope{Type: "transfer", ResourceKind: "transfer", ResourceID: "job-1"})
	if event := <-firstStream; event.Sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", event.Sequence)
	}
	unsubscribeFirst()
	manager.ReleaseEvents("owner-1")

	secondStream, unsubscribeSecond := manager.Events("owner-1").Subscribe(1)
	defer unsubscribeSecond()
	manager.PublishEvent("owner-1", events.Envelope{Type: "transfer", ResourceKind: "transfer", ResourceID: "job-1"})
	if event := <-secondStream; event.Sequence != 2 {
		t.Fatalf("reconnected sequence = %d, want 2", event.Sequence)
	}
}
