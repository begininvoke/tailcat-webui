package events

import (
	"encoding/json/v2"
	"testing"
	"time"
)

func TestEnvelopeJSONContract(t *testing.T) {
	at := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	envelope := Envelope{
		Version:      1,
		Type:         "runtime",
		ResourceKind: "client",
		ResourceID:   "client-1",
		Phase:        RuntimePhaseReady,
		Sequence:     7,
		At:           at,
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "type", "resource_kind", "resource_id", "phase", "sequence", "at"} {
		if _, ok := got[field]; !ok {
			t.Errorf("missing %q in envelope JSON", field)
		}
	}
	for _, field := range []string{"operation_id", "payload", "user_id"} {
		if _, ok := got[field]; ok {
			t.Errorf("unexpected %q in envelope JSON", field)
		}
	}
}
