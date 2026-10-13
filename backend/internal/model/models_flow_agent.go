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
	ExternalID        string    `json:"externalId,omitempty" gorm:"size:80"` // canvas sidebar conversation id (subject canvas)
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
	ExternalID     string    `json:"externalId,omitempty" gorm:"size:80;index"` // id of the mirrored canvas sidebar message
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

// FlowSkillInstall pins one version of a skill (from an embedded skill pack)
// for a workspace. Hash is the sha256 of the canonical skill JSON.
type FlowSkillInstall struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:idx_flow_skill_ws,priority:1"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;not null;uniqueIndex:idx_flow_skill_ws,priority:2"`
	SkillID     string    `json:"skillId" gorm:"size:80;not null;uniqueIndex:idx_flow_skill_ws,priority:3"`
	Version     string    `json:"version" gorm:"size:40;not null"`
	Hash        string    `json:"hash" gorm:"size:64;not null"`
	PackID      string    `json:"packId" gorm:"size:80"`
	InstalledBy string    `json:"installedBy" gorm:"size:64"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// FlowSkillRunPin freezes the skill version/hash used by one agent run.
type FlowSkillRunPin struct {
	RunID     string    `json:"runId" gorm:"primaryKey;size:80"`
	SkillID   string    `json:"skillId" gorm:"primaryKey;size:80"`
	TenantID  string    `json:"tenantId" gorm:"size:40;not null"`
	Version   string    `json:"version" gorm:"size:40;not null"`
	Hash      string    `json:"hash" gorm:"size:64;not null"`
	CreatedAt time.Time `json:"createdAt"`
}

// FlowNotification is an in-app notification for a workspace (e.g. report completed).
type FlowNotification struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string     `json:"tenantId" gorm:"size:40;not null;index:idx_flow_notif_ws,priority:1"`
	WorkspaceID string     `json:"workspaceId" gorm:"size:40;not null;index:idx_flow_notif_ws,priority:2"`
	EventID     string     `json:"eventId" gorm:"size:64;not null;uniqueIndex"`
	Type        string     `json:"type" gorm:"size:40;not null"`
	Title       string     `json:"title" gorm:"size:300"`
	RefID       string     `json:"refId" gorm:"size:64"`
	ReadAt      *time.Time `json:"readAt"`
	CreatedAt   time.Time  `json:"createdAt" gorm:"index:idx_flow_notif_ws,priority:3"`
}

// FlowMCPConnection is an operator-approved remote MCP server for one workspace.
// CredentialRef names a server-side secret; the secret itself is never stored
// here, rendered to the frontend, or placed in prompts/skills/tool output.
type FlowMCPConnection struct {
	ID              string    `json:"id" gorm:"primaryKey;size:40"`
	Slug            string    `json:"slug" gorm:"size:16;not null;uniqueIndex"`
	TenantID        string    `json:"tenantId" gorm:"size:40;not null;index:idx_flow_mcp_ws,priority:1"`
	WorkspaceID     string    `json:"workspaceId" gorm:"size:40;not null;index:idx_flow_mcp_ws,priority:2"`
	Name            string    `json:"name" gorm:"size:120"`
	Endpoint        string    `json:"endpoint" gorm:"size:500;not null"`
	Transport       string    `json:"transport" gorm:"size:32;not null"`
	CredentialRef   string    `json:"credentialRef" gorm:"size:120"`
	AllowedTools    string    `json:"-" gorm:"type:text"` // JSON []string
	ApprovedSchemas string    `json:"-" gorm:"type:text"` // JSON map[tool]{hash,description,inputSchema}
	TimeoutMS       int       `json:"timeoutMs"`
	OutputCap       int       `json:"outputCap"`
	CostRule        string    `json:"-" gorm:"type:text"`             // JSON {per_call_credits, max_calls_per_day}
	Status          string    `json:"status" gorm:"size:24;not null"` // pending|approved|needs_reapproval|revoked
	StatusReason    string    `json:"statusReason" gorm:"size:300"`
	Version         int       `json:"version"`
	CreatedBy       string    `json:"createdBy" gorm:"size:36"`
	ApprovedBy      string    `json:"approvedBy" gorm:"size:36"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// FlowMCPUsage records each gateway tool execution (cost/budget accounting).
type FlowMCPUsage struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID     string    `json:"tenantId" gorm:"size:40;not null;index:idx_flow_mcp_usage,priority:1"`
	ConnectionID string    `json:"connectionId" gorm:"size:40;not null;index:idx_flow_mcp_usage,priority:2"`
	Tool         string    `json:"tool" gorm:"size:120"`
	ActorID      string    `json:"actorId" gorm:"size:36"`
	Status       string    `json:"status" gorm:"size:24"`
	DurationMS   int64     `json:"durationMs"`
	OutputBytes  int       `json:"outputBytes"`
	Credits      int       `json:"credits"`
	CreatedAt    time.Time `json:"createdAt" gorm:"index:idx_flow_mcp_usage,priority:3"`
}
