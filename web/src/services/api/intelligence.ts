import { http } from "./request";

export type AgentSubject = { kind: "canvas" | "intelligence_workspace" | "topic"; id: string };
export type IntelWorkspace = { id: string; tenantId: string; slug: string; name: string };
export type FlowConversation = { id: string; title: string; subjectKind: string; subjectId: string; workspaceId: string; updatedAt: string; lastSeq: number };
export type FlowMessage = { id: string; seq: number; role: string; content: string; toolName?: string; revision?: number; editedAt?: string; createdAt: string };
export type ToolResult = { callId: string; tool: string; status: string; approvalId?: string; replayed?: boolean; output?: unknown; error?: string };
export type IntelMonitor = { id: string; name: string; keywords: string[]; source_ids: string[]; interval_seconds: number; timezone: string; created_at: string };

const CACHE_KEY = "flow.conversations.cache.v1";

export const listIntelWorkspaces = () => http.get<IntelWorkspace[]>("/intelligence/workspaces");

/** Server directory is authoritative; localStorage is only a warm-start cache. */
export async function listConversations(): Promise<FlowConversation[]> {
    const list = await http.get<FlowConversation[]>("/agent/conversations");
    try {
        localStorage.setItem(CACHE_KEY, JSON.stringify(list));
    } catch {
        /* cache is optional */
    }
    return list;
}
export function cachedConversations(): FlowConversation[] {
    try {
        return JSON.parse(localStorage.getItem(CACHE_KEY) || "[]") as FlowConversation[];
    } catch {
        return [];
    }
}
export const createConversation = (subject: AgentSubject, title: string) => http.post<FlowConversation>("/agent/conversations", { subject, title });
export const listMessages = (id: string, beforeSeq?: number) => http.get<{ messages: FlowMessage[]; nextBeforeSeq: number }>(`/agent/conversations/${encodeURIComponent(id)}/messages`, { params: beforeSeq ? { before_seq: beforeSeq } : {} });
export const postMessage = (id: string, content: string) => http.post<FlowMessage>(`/agent/conversations/${encodeURIComponent(id)}/messages`, { content });
/** Edit / delete (tombstone) a message; baseRevision gives optimistic concurrency (409 on conflict). Canvas conversations sync back. */
export const editMessage = (id: string, msgId: string, content: string, baseRevision?: number) => http.patch<FlowMessage>(`/agent/conversations/${encodeURIComponent(id)}/messages/${encodeURIComponent(msgId)}`, { content, baseRevision });
export const deleteMessage = (id: string, msgId: string) => http.delete<FlowMessage>(`/agent/conversations/${encodeURIComponent(id)}/messages/${encodeURIComponent(msgId)}`);
export const callTool = (id: string, callId: string, name: string, args: Record<string, unknown>) => http.post<ToolResult>(`/agent/conversations/${encodeURIComponent(id)}/tool-calls`, { callId, name, arguments: args });
export const listMonitors = (workspaceId: string) => http.get<IntelMonitor[]>("/intelligence/monitors", { headers: { "X-Workspace-Id": workspaceId } });

export type ReportCitation = { evidence_id: string; document_id: string; source_id: string; title?: string; revision: number; chunk_index: number; quote: string; truncated: boolean; chunk_text?: string | null };
export type ReportClaim = { idx: number; text: string; kind: "source_statement" | "cross_source_verified" | "model_inference"; citations: ReportCitation[] };
export type ReportCoverage = { truncated?: boolean; truncated_citations?: string[]; source_failures?: { source_id: string; error: string }[]; conflicting_evidence?: { document_id: string; revision: number; note: string }[] };
export type ReportSummary = { id: string; kind: "report" | "brief"; title: string; coverage: ReportCoverage; claims: number; created_at: string; scheduled_at?: string };
export type ReportDetail = ReportSummary & { body: string; skill: { id?: string; version?: string; hash?: string }; claims: ReportClaim[] };
export type BriefSchedule = { id: string; name: string; query: string; cron: string; timezone: string; window_hours: number; next_run_at: string; enabled: boolean };
const ws = (workspaceId: string) => ({ headers: { "X-Workspace-Id": workspaceId } });
export const listReports = (workspaceId: string) => http.get<ReportSummary[]>("/intelligence/reports", ws(workspaceId));
export const getReport = (workspaceId: string, id: string) => http.get<ReportDetail>(`/intelligence/reports/${encodeURIComponent(id)}`, ws(workspaceId));
export const listBriefSchedules = (workspaceId: string) => http.get<BriefSchedule[]>("/intelligence/brief-schedules", ws(workspaceId));

export type SourceKind = "rss" | "telegram" | "wechat";
export type IntelSource = { id: string; kind: SourceKind; url: string; title: string; status: string; last_error?: string | null; config?: Record<string, unknown> };
export type ConnectorSample = { id: string; kind: string; source_id: string; collector: string; provider?: string | null; mode: "live" | "fixture"; items: number; media_unsupported: number; recorded_at: string };
export const listSources = (workspaceId: string) => http.get<IntelSource[]>("/intelligence/sources", ws(workspaceId));
export const createSource = (workspaceId: string, input: { kind: SourceKind; url?: string; title?: string; config?: Record<string, unknown> }) => http.post<IntelSource>("/intelligence/sources", input, ws(workspaceId));
export const revokeSource = (workspaceId: string, id: string) => http.post<{ status: string }>(`/intelligence/sources/${encodeURIComponent(id)}/revoke`, { reason: "revoked in UI" }, ws(workspaceId));
export const listConnectorSamples = (workspaceId: string) => http.get<ConnectorSample[]>("/intelligence/connector-samples", ws(workspaceId));

export type McpTool = { name: string; description: string; hash: string };
export type McpConnection = {
    id: string;
    name: string;
    endpoint: string;
    transport: string;
    credentialRef: string;
    status: "pending" | "approved" | "needs_reapproval" | "revoked";
    statusReason: string;
    version: number;
    timeoutMs: number;
    outputCap: number;
    allowedTools: string[] | null;
    costRule: { per_call_credits?: number; max_calls_per_day?: number };
};
export const listMcpConnections = (workspaceId: string) => http.get<McpConnection[]>("/intelligence/mcp/connections", ws(workspaceId));
export const createMcpConnection = (workspaceId: string, input: { name: string; endpoint: string; credentialRef?: string; timeoutMs?: number; outputCap?: number; costRule?: { per_call_credits?: number; max_calls_per_day?: number } }) =>
    http.post<McpConnection>("/intelligence/mcp/connections", input, ws(workspaceId));
export const discoverMcpTools = (workspaceId: string, id: string) => http.get<McpTool[]>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/tools`, ws(workspaceId));
export const approveMcpConnection = (workspaceId: string, id: string, allowedTools: string[]) => http.post<McpConnection>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/approve`, { allowedTools }, ws(workspaceId));
export const revokeMcpConnection = (workspaceId: string, id: string) => http.post<{ revoked: boolean }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/revoke`, {}, ws(workspaceId));

export type FlowNotification = { id: string; type: string; title: string; refId: string; readAt?: string | null; createdAt: string };
export const listNotifications = (workspaceId: string) => http.get<FlowNotification[]>("/intelligence/notifications", ws(workspaceId));
export const markNotificationRead = (workspaceId: string, id: string | "all") => http.post<{ updated: number }>(`/intelligence/notifications/${encodeURIComponent(id)}/read`, {}, ws(workspaceId));

export type SkillDef = { id: string; version: string; name: string; method: string; hash: string; pack_id: string };
export type SkillPack = { id: string; version: string; name: string; kind?: string; skills: SkillDef[] };
export type SkillInstall = { id: string; skillId: string; version: string; hash: string; packId: string; updatedAt: string };
export const listSkills = (workspaceId: string) => http.get<{ packs: SkillPack[]; installed: SkillInstall[] }>("/intelligence/skills", ws(workspaceId));
export const installSkills = (workspaceId: string, input: { packId?: string; skillId?: string; version?: string }) => http.post<SkillInstall[]>("/intelligence/skills/install", input, ws(workspaceId));

export type BillingAccount = { id: string; tenantId: string; payerUserId: string; kind: "personal" | "shared"; monthlyQuotaMicro: number; maxOpenReserves: number };
export type BudgetReservation = { id: string; purpose: string; parentId: string; reservedMicro: number; allocatedMicro: number; consumedMicro: number; status: string; createdAt: string };
export const getBilling = (workspaceId: string) => http.get<{ account: BillingAccount; reservations: BudgetReservation[] }>("/intelligence/billing", ws(workspaceId));
export const createOrganization = (name: string) => http.post<{ tenant: { id: string }; workspace: IntelWorkspace }>("/intelligence/orgs", { name });
export const createInvite = (workspaceId: string, email: string, role: string) => http.post<{ invite: { id: string; email: string; role: string; expiresAt: string }; token: string }>("/intelligence/invites", { email, role }, ws(workspaceId));
export const acceptInvite = (token: string) => http.post<{ tenantId: string; workspaceId: string; role: string }>("/intelligence/invites/accept", { token });

export type OrgMember = { userId: string; email: string; name: string; role: "owner" | "admin" | "member"; status: string; joinedAt: string };
export const listMembers = (workspaceId: string) => http.get<{ members: OrgMember[]; canManage: boolean }>("/intelligence/members", ws(workspaceId));
export const setMemberRole = (workspaceId: string, userId: string, role: "admin" | "member" | "viewer") => http.patch<{ ok: boolean }>(`/intelligence/members/${encodeURIComponent(userId)}`, { role }, ws(workspaceId));
export const resolveReservation = (workspaceId: string, id: string, outcome: "succeeded" | "not_executed", costMicro?: number) =>
    http.post<BudgetReservation>(`/intelligence/billing/reservations/${encodeURIComponent(id)}/resolve`, { outcome, costMicro }, ws(workspaceId));

export type McpCall = { id: string; connectionId: string; tool: string; status: "pending" | "succeeded" | "failed" | "unknown"; billed: boolean; attempts: number; idemKey: string; createdAt: string; updatedAt: string };
export const listMcpCalls = (workspaceId: string, status?: string) => http.get<McpCall[]>("/intelligence/mcp/calls", { ...ws(workspaceId), params: status ? { status } : {} });
export const resolveMcpCall = (workspaceId: string, id: string, outcome: "succeeded" | "not_executed", output?: string) => http.post<McpCall>(`/intelligence/mcp/calls/${encodeURIComponent(id)}/resolve`, { outcome, output }, ws(workspaceId));

export const startMcpOAuth = (workspaceId: string, id: string) => http.post<{ authorizeUrl: string }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/oauth/start`, {}, ws(workspaceId));
export const getMcpOAuth = (workspaceId: string, id: string) => http.get<{ connected: boolean; expiresAt?: string; scope?: string }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/oauth`, ws(workspaceId));
export const revokeMcpOAuth = (workspaceId: string, id: string) => http.post<{ revoked: boolean }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/oauth/revoke`, {}, ws(workspaceId));
