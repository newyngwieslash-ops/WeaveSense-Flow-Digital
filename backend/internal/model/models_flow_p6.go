package model

import "time"

// FlowMCPCall makes a remote MCP tool call idempotent by key. An "unknown"
// call (request sent, result lost) is reconciled before any retry and billed
// at most once.
type FlowMCPCall struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID     string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_flow_mcp_call,priority:1"`
	IdemKey      string    `json:"idemKey" gorm:"size:200;not null;uniqueIndex:ux_flow_mcp_call,priority:2"`
	ConnectionID string    `json:"connectionId" gorm:"size:40;not null"`
	Tool         string    `json:"tool" gorm:"size:120"`
	ArgsHash     string    `json:"argsHash" gorm:"size:64"`
	Status       string    `json:"status" gorm:"size:16;not null"` // pending | succeeded | failed | unknown
	Output       string    `json:"-" gorm:"type:text"`
	Truncated    bool      `json:"truncated"`
	Billed       bool      `json:"billed"`
	Attempts     int       `json:"attempts"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// FlowRunSlot admits runs/executors per owner up to a concurrency limit; the
// rest wait in FIFO order (runtime busy => queue, not reject).
type FlowRunSlot struct {
	ID        string    `json:"id" gorm:"primaryKey;size:120"`
	Owner     string    `json:"owner" gorm:"size:80;not null;index:idx_flow_slot_owner,priority:1"`
	Kind      string    `json:"kind" gorm:"size:24;not null;index:idx_flow_slot_owner,priority:2"`
	UserID    string    `json:"userId" gorm:"size:36"`
	Status    string    `json:"status" gorm:"size:16;not null"` // queued | running
	CreatedAt time.Time `json:"createdAt" gorm:"index"`
}

// FlowExternalWait parks an agent run on a long-running engine operation
// (waiting_external). Completion resumes it exactly once.
type FlowExternalWait struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string     `json:"tenantId" gorm:"size:40;not null"`
	WorkspaceID string     `json:"workspaceId" gorm:"size:40;not null"`
	UserID      string     `json:"userId" gorm:"size:36;not null"`
	RunID       string     `json:"runId" gorm:"size:80;not null;index"`
	ToolCallID  string     `json:"toolCallId" gorm:"size:120"`
	OperationID string     `json:"operationId" gorm:"size:64;not null;uniqueIndex"`
	Reservation string     `json:"reservation" gorm:"size:160"`
	Checkpoint  string     `json:"-" gorm:"type:text"`
	Status      string     `json:"status" gorm:"size:16;not null;index"` // waiting | ready | resuming | resumed | denied
	Outcome     string     `json:"outcome" gorm:"size:24"`
	ResumeRunID string     `json:"resumeRunId" gorm:"size:80"`
	CreatedAt   time.Time  `json:"createdAt"`
	ResumedAt   *time.Time `json:"resumedAt"`
}
