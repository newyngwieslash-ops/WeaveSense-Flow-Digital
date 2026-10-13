package model

import "time"

// FlowTopic is a named analysis topic inside an intelligence workspace.
type FlowTopic struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;index"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;index"`
	Title       string    `json:"title" gorm:"size:200"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (FlowTopic) TableName() string { return "flow_topics" }

// FlowAgentConversation is the server-side conversation directory entry.
// CloudAgentExecution.ConversationID links runs/events/Pi sessions to it.
type FlowAgentConversation struct {
	ID                string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID          string    `json:"tenantId" gorm:"size:40;index:ix_flow_conv_owner,priority:1"`
	WorkspaceID       string    `json:"workspaceId" gorm:"size:40"`
	UserID            string    `json:"userId" gorm:"size:36;index:ix_flow_conv_owner,priority:2"`
	SubjectKind       string    `json:"subjectKind" gorm:"size:32"`
	SubjectID         string    `json:"subjectId" gorm:"size:80"`
	Title             string    `json:"title" gorm:"size:240"`
	Summary           string    `json:"summary" gorm:"type:text"`
	SummaryThroughSeq int64     `json:"summaryThroughSeq"`
	CurrentTopicID    string    `json:"currentTopicId" gorm:"size:40"`
	EvidenceScope     string    `json:"evidenceScope" gorm:"type:text"`  // JSON: {document_ids, source_ids, from, to}
	ConfirmedPlans    string    `json:"confirmedPlans" gorm:"type:text"` // JSON array
	PiSessionID       string    `json:"piSessionId" gorm:"size:80"`
	LastSeq           int64     `json:"lastSeq"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt" gorm:"index"`
}

func (FlowAgentConversation) TableName() string { return "flow_agent_conversations" }

type FlowAgentMessage struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	ConversationID string    `json:"conversationId" gorm:"size:40;uniqueIndex:ux_flow_msg_seq,priority:1"`
	Seq            int64     `json:"seq" gorm:"uniqueIndex:ux_flow_msg_seq,priority:2"`
	TenantID       string    `json:"-" gorm:"size:40;index"`
	Role           string    `json:"role" gorm:"size:16"` // user | assistant | tool | system
	Content        string    `json:"content" gorm:"type:text"`
	RunID          string    `json:"runId,omitempty" gorm:"size:80"`
	ToolCallID     string    `json:"toolCallId,omitempty" gorm:"size:80"`
	ToolName       string    `json:"toolName,omitempty" gorm:"size:80"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (FlowAgentMessage) TableName() string { return "flow_agent_messages" }

// FlowAgentToolCall stores one tool execution for idempotent replay and receipts.
type FlowAgentToolCall struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID       string    `json:"tenantId" gorm:"size:40;uniqueIndex:ux_flow_tool_call,priority:1"`
	ConversationID string    `json:"conversationId" gorm:"size:40;uniqueIndex:ux_flow_tool_call,priority:2"`
	CallID         string    `json:"callId" gorm:"size:80;uniqueIndex:ux_flow_tool_call,priority:3"`
	ToolName       string    `json:"toolName" gorm:"size:80"`
	Class          string    `json:"class" gorm:"size:8"`
	RequestHash    string    `json:"-" gorm:"size:64"`
	Arguments      string    `json:"arguments" gorm:"type:text"`
	Status         string    `json:"status" gorm:"size:24"` // succeeded | failed | waiting_approval | rejected
	ApprovalID     string    `json:"approvalId,omitempty" gorm:"size:40;index"`
	Result         string    `json:"result" gorm:"type:text"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (FlowAgentToolCall) TableName() string { return "flow_agent_tool_calls" }
