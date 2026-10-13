import { http } from "./request";

export type AgentSubject = { kind: "canvas" | "intelligence_workspace" | "topic"; id: string };
export type IntelWorkspace = { id: string; tenantId: string; slug: string; name: string };
export type FlowConversation = { id: string; title: string; subjectKind: string; subjectId: string; workspaceId: string; updatedAt: string; lastSeq: number };
export type FlowMessage = { id: string; seq: number; role: string; content: string; toolName?: string; createdAt: string };
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
