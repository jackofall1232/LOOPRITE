package orchestration

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateEvent(t *testing.T) {
	e := OrchestrationEvent{SchemaVersion: SchemaVersion, ID: "evt_1", CorrelationID: "collab_1",
		Kind: "task.completed", OccurredAt: time.Now(), Trust: TrustProvider, Payload: json.RawMessage(`{"ok":true}`)}
	if err := ValidateEvent(e); err != nil {
		t.Fatal(err)
	}
	e.Trust = "trusted_because_model_said_so"
	if err := ValidateEvent(e); err == nil {
		t.Fatal("unknown trust label accepted")
	}
}

func TestValidateCapabilityRequiresSchemas(t *testing.T) {
	c := CapabilityDescriptor{SchemaVersion: SchemaVersion, ID: "repo.read", Version: "1",
		InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"string"}`), Trust: TrustBroker}
	if err := ValidateCapability(c); err != nil {
		t.Fatal(err)
	}
	c.OutputSchema = nil
	if err := ValidateCapability(c); err == nil {
		t.Fatal("missing output schema accepted")
	}
}
