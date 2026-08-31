package tailnet

import (
	"reflect"
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
