// Package orchestration defines the provider-neutral contracts that later phases will persist and
// execute. Phase 0 deliberately contains no scheduler or live request routing.
package orchestration

import (
	"encoding/json"
	"time"
)

const SchemaVersion = 1

type TrustLabel string

const (
	TrustAuthenticated TrustLabel = "authenticated_user"
	TrustBroker        TrustLabel = "broker"
	TrustProvider      TrustLabel = "untrusted_provider"
	TrustRepository    TrustLabel = "untrusted_repository"
)

type OrchestrationEvent struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	CorrelationID string          `json:"correlation_id"`
	Kind          string          `json:"kind"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Trust         TrustLabel      `json:"trust"`
	Payload       json.RawMessage `json:"payload"`
}

type ApprovalRequest struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	CorrelationID string          `json:"correlation_id"`
	CapabilityID  string          `json:"capability_id"`
	RiskClass     string          `json:"risk_class"`
	Arguments     json.RawMessage `json:"arguments"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

type CapabilityDescriptor struct {
	SchemaVersion   int             `json:"schema_version"`
	ID              string          `json:"id"`
	Version         string          `json:"version"`
	InputSchema     json.RawMessage `json:"input_schema"`
	OutputSchema    json.RawMessage `json:"output_schema"`
	RequiredScopes  []string        `json:"required_scopes"`
	SideEffectClass string          `json:"side_effect_class"`
	Trust           TrustLabel      `json:"trust"`
}

type ToolGrant struct {
	SchemaVersion   int       `json:"schema_version"`
	ID              string    `json:"id"`
	Project         string    `json:"project"`
	Repo            string    `json:"repo,omitempty"`
	CollaborationID string    `json:"collaboration_id"`
	TaskID          string    `json:"task_id"`
	CapabilityID    string    `json:"capability_id"`
	MaxInvocations  int       `json:"max_invocations"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type CollaborationRun struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Project       string    `json:"project"`
	Repo          string    `json:"repo,omitempty"`
	Goal          string    `json:"goal"`
	Status        string    `json:"status"`
	BudgetUSD     *float64  `json:"budget_usd,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type DelegationTask struct {
	SchemaVersion   int             `json:"schema_version"`
	ID              string          `json:"id"`
	CollaborationID string          `json:"collaboration_id"`
	ParentTaskID    string          `json:"parent_task_id,omitempty"`
	Outcome         string          `json:"outcome"`
	Input           json.RawMessage `json:"input"`
	OutputSchema    json.RawMessage `json:"output_schema"`
	Deadline        time.Time       `json:"deadline"`
}

type TaskAttempt struct {
	SchemaVersion     int       `json:"schema_version"`
	ID                string    `json:"id"`
	TaskID            string    `json:"task_id"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Runtime           string    `json:"runtime"`
	ExternalSessionID string    `json:"external_session_id,omitempty"`
	Status            string    `json:"status"`
	StartedAt         time.Time `json:"started_at"`
}

type Artifact struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	AttemptID     string          `json:"attempt_id"`
	Kind          string          `json:"kind"`
	MediaType     string          `json:"media_type"`
	SHA256        string          `json:"sha256"`
	Trust         TrustLabel      `json:"trust"`
	Content       json.RawMessage `json:"content"`
}

type ExternalSession struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Runtime       string    `json:"runtime"`
	ExternalID    string    `json:"external_id"`
	ResumeCursor  string    `json:"resume_cursor,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}
