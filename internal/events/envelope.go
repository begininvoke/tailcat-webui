package events

import "time"

type RuntimePhase string

const (
	RuntimePhaseIdle        RuntimePhase = "idle"
	RuntimePhaseStarting    RuntimePhase = "starting"
	RuntimePhaseConnecting  RuntimePhase = "connecting"
	RuntimePhaseReady       RuntimePhase = "ready"
	RuntimePhaseRunning     RuntimePhase = "running"
	RuntimePhaseStopping    RuntimePhase = "stopping"
	RuntimePhaseStopped     RuntimePhase = "stopped"
	RuntimePhaseError       RuntimePhase = "error"
	RuntimePhaseInterrupted RuntimePhase = "interrupted"
)

type Envelope struct {
	Version      int          `json:"version"`
	Type         string       `json:"type"`
	ResourceKind string       `json:"resource_kind"`
	ResourceID   string       `json:"resource_id"`
	OperationID  string       `json:"operation_id,omitempty"`
	Phase        RuntimePhase `json:"phase"`
	Sequence     uint64       `json:"sequence"`
	At           time.Time    `json:"at"`
	Payload      any          `json:"payload,omitempty"`
}
