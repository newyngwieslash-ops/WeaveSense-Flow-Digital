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
export const decideApproval = (approvalId: string, approve: boolean) => http.post<ToolResult>(`/agent/approvals/${encodeURIComponent(approvalId)}/decision`, { approve });
export const listMonitors = (workspaceId: string) => http.get<IntelMonitor[]>("/intelligence/monitors", { headers: { "X-Workspace-Id": workspaceId } });
