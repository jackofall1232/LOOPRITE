package orchestration

import (
	"errors"
	"strings"
	"time"
)

func ValidTrust(v TrustLabel) bool {
	switch v {
	case TrustAuthenticated, TrustBroker, TrustProvider, TrustRepository:
		return true
	}
	return false
}

func ValidateEvent(e OrchestrationEvent) error {
	if e.SchemaVersion != SchemaVersion {
		return errors.New("unsupported schema_version")
	}
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.CorrelationID) == "" || strings.TrimSpace(e.Kind) == "" {
		return errors.New("id, correlation_id, and kind are required")
	}
	if e.OccurredAt.IsZero() || e.OccurredAt.After(time.Now().Add(5*time.Minute)) {
		return errors.New("invalid occurred_at")
	}
	if !ValidTrust(e.Trust) {
		return errors.New("invalid trust label")
	}
	if len(e.Payload) == 0 {
		return errors.New("payload is required")
	}
	return nil
}

func ValidateCapability(c CapabilityDescriptor) error {
	if c.SchemaVersion != SchemaVersion {
		return errors.New("unsupported schema_version")
	}
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Version) == "" {
		return errors.New("id and version are required")
	}
	if len(c.InputSchema) == 0 || len(c.OutputSchema) == 0 {
		return errors.New("input and output schemas are required")
	}
	if !ValidTrust(c.Trust) {
		return errors.New("invalid trust label")
	}
	return nil
}
