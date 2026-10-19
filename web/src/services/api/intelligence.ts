import { useUserStore } from "@/stores/use-user-store";
import { http } from "./request";

export type AgentSubject = { kind: "canvas" | "intelligence_workspace" | "topic"; id: string };
export type IntelWorkspace = { id: string; tenantId: string; slug: string; name: string };
export type FlowConversation = { id: string; title: string; subjectKind: string; subjectId: string; workspaceId: string; updatedAt: string; lastSeq: number };
export type FlowMessage = { id: string; seq: number; role: string; content: string; toolName?: string; revision?: number; editedAt?: string; createdAt: string };
export type ToolResult = { callId: string; tool: string; status: string; approvalId?: string; replayed?: boolean; output?: unknown; error?: string };
export type IntelMonitor = {
    id: string;
    name: string;
    keywords: string[];
    source_ids: string[];
    interval_seconds: number;
    timezone: string;
    created_at: string;
    budget_micro_per_window?: number;
    notification_target_ids?: string[];
    extraction_method?: string;
    version?: number;
};

const CACHE_KEY_PREFIX = "flow.conversations.cache.v1";

export function conversationCacheKey(userId: string | null | undefined = useUserStore.getState().user?.id): string | null {
    const id = userId?.trim();
    return id ? `${CACHE_KEY_PREFIX}:${id}` : null;
}

export function writeCachedConversations(list: FlowConversation[], userId?: string | null): void {
    const key = conversationCacheKey(userId);
    if (!key) return;
    try {
        localStorage.setItem(key, JSON.stringify(list));
    } catch {
        /* cache is optional */
    }
}

export const listIntelWorkspaces = () => http.get<IntelWorkspace[]>("/intelligence/workspaces");

/** Server directory is authoritative; localStorage is only a user-scoped warm-start cache. */
export async function listConversations(userId?: string | null): Promise<FlowConversation[]> {
    const list = await http.get<FlowConversation[]>("/agent/conversations");
    writeCachedConversations(list, userId);
    return list;
}
export function cachedConversations(userId?: string | null): FlowConversation[] {
    const key = conversationCacheKey(userId);
    if (!key) return [];
    try {
        return JSON.parse(localStorage.getItem(key) || "[]") as FlowConversation[];
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

export type OrgMember = { userId: string; email: string; name: string; role: "owner" | "admin" | "editor" | "member" | "viewer"; status: string; joinedAt: string };
export const listMembers = (workspaceId: string) => http.get<{ members: OrgMember[]; canManage: boolean }>("/intelligence/members", ws(workspaceId));
export const setMemberRole = (workspaceId: string, userId: string, role: "admin" | "editor" | "member" | "viewer") => http.patch<{ ok: boolean }>(`/intelligence/members/${encodeURIComponent(userId)}`, { role }, ws(workspaceId));
export const resolveReservation = (workspaceId: string, id: string, outcome: "succeeded" | "not_executed", costMicro?: number) =>
    http.post<BudgetReservation>(`/intelligence/billing/reservations/${encodeURIComponent(id)}/resolve`, { outcome, costMicro }, ws(workspaceId));

export type McpCall = { id: string; connectionId: string; tool: string; status: "pending" | "succeeded" | "failed" | "unknown"; billed: boolean; attempts: number; idemKey: string; createdAt: string; updatedAt: string };
export const listMcpCalls = (workspaceId: string, status?: string) => http.get<McpCall[]>("/intelligence/mcp/calls", { ...ws(workspaceId), params: status ? { status } : {} });
export const resolveMcpCall = (workspaceId: string, id: string, outcome: "succeeded" | "not_executed", output?: string) => http.post<McpCall>(`/intelligence/mcp/calls/${encodeURIComponent(id)}/resolve`, { outcome, output }, ws(workspaceId));

export const startMcpOAuth = (workspaceId: string, id: string) => http.post<{ authorizeUrl: string }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/oauth/start`, {}, ws(workspaceId));
export const getMcpOAuth = (workspaceId: string, id: string) => http.get<{ connected: boolean; expiresAt?: string; scope?: string }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/oauth`, ws(workspaceId));
export const revokeMcpOAuth = (workspaceId: string, id: string) => http.post<{ revoked: boolean }>(`/intelligence/mcp/connections/${encodeURIComponent(id)}/oauth/revoke`, {}, ws(workspaceId));

/** PRD §5.1 views: sources health, jobs, schedules, content library, keywords (term aliases). */
export type SourceHealth = IntelSource & {
    health: "healthy" | "degraded" | "backoff" | "never_run" | "paused" | "revoked" | string;
    paused_reason?: string | null;
    consecutive_failures?: number;
    backoff_until?: string | null;
    last_run?: { run_id: string; status: string; items_new: number; items_revised: number; error?: string | null; finished_at?: string | null } | null;
};
export type CollectionJob = {
    job_id: string;
    operation_id: string;
    source_id: string;
    source_title?: string | null;
    source_kind?: string | null;
    schedule_id?: string | null;
    scheduled_at?: string | null;
    status: string;
    job_state: string;
    items_new: number;
    items_revised: number;
    error?: string | null;
    partial_reason?: string | null;
    attempts: number;
    retry_at?: string | null;
    created_at: string;
    finished_at?: string | null;
};
export type CollectionSchedule = {
    id: string;
    source_ids: string[];
    kind: "interval" | "cron";
    interval_seconds?: number | null;
    cron?: string | null;
    timezone: string;
    next_run_at: string;
    enabled: boolean;
    state?: "active" | "blocked" | "disabled";
    blocked_reason?: string | null;
    disabled_reason?: string | null;
    budget_micro_per_window?: number;
};
export type ExtractionCompleteness = {
    level: "full_text" | "feed_summary" | "title_only";
    method: string;
    body_chars: number;
    media_total: number;
    media_stored: number;
    media_missing: number;
    complete: boolean;
};
export type MediaObject = { object_key: string | null; type: string; status: string };
export type LibraryDocument = {
    id: string;
    source_id: string;
    source_title?: string | null;
    source_kind?: string | null;
    title: string;
    url: string;
    published_at?: string | null;
    event_occurred_at?: string | null;
    collected_at?: string;
    current_revision: number;
    extraction?: ExtractionCompleteness;
    media_objects?: MediaObject[];
};
export type TermAlias = { term: string; alias: string; created_by?: string; created_at?: string };
export const listSourcesHealth = (workspaceId: string) => http.get<SourceHealth[]>("/intelligence/sources/health", ws(workspaceId));
export const listJobs = (workspaceId: string, status?: string) => http.get<CollectionJob[]>("/intelligence/collection-runs", { ...ws(workspaceId), params: status ? { status } : {} });
export const listSchedules = (workspaceId: string) => http.get<CollectionSchedule[]>("/intelligence/schedules", ws(workspaceId));
export const listDocuments = (workspaceId: string, q?: string) => http.get<LibraryDocument[]>("/intelligence/documents", { ...ws(workspaceId), params: q ? { q } : {} });
export const listTerms = (workspaceId: string) => http.get<{ builtin_version: string; builtin_groups: number; builtin: string[][]; custom: TermAlias[] }>("/intelligence/terms", ws(workspaceId));
export const addTerm = (workspaceId: string, term: string, alias: string) => http.post<{ added: number }>("/intelligence/terms", { items: [{ term, alias }] }, ws(workspaceId));
export const deleteTerm = (workspaceId: string, term: string, alias: string) => http.post<{ deleted: number }>("/intelligence/terms/delete", { term, alias }, ws(workspaceId));

/** PRD §5.2 saved research topics. */
export type IntelTopic = { id: string; workspaceId: string; title: string; question: string; keywords: string[]; sourceIds: string[]; notes: string; version: number; createdAt: string; updatedAt: string };
export type TopicInput = { title?: string; question?: string; keywords?: string[]; sourceIds?: string[]; notes?: string };
export const listTopics = (workspaceId: string) => http.get<IntelTopic[]>("/intelligence/topics", ws(workspaceId));
export const createTopic = (workspaceId: string, input: TopicInput) => http.post<IntelTopic>("/intelligence/topics", input, ws(workspaceId));
export const updateTopic = (workspaceId: string, id: string, input: TopicInput, ifVersion?: number) => http.patch<IntelTopic>(`/intelligence/topics/${encodeURIComponent(id)}`, { ...input, ifVersion }, ws(workspaceId));

/** PRD §5.1 structured monitor plan (preview) and update with re-approval on expansion. */
export type MonitorPlan = {
    preview_id: string;
    name: string;
    keywords: string[];
    source_ids: string[];
    sources?: { id: string; kind: string; title: string; url: string }[];
    interval_seconds: number;
    timezone: string;
    misfire_policy?: string;
    extraction_method?: string;
    budget_micro_per_window?: number;
    notification_target_ids?: string[];
    cost_estimate?: { price_version: string; per_window_micro: number; per_day_micro: number; per_30d_micro: number; basis: string };
    runs_per_day?: number;
    monitor_id?: string | null;
    expansion?: string[];
    requires_approval?: boolean;
    expires_at?: string;
};
export const getMonitorPreview = (workspaceId: string, id: string) => http.get<MonitorPlan>(`/intelligence/monitors/previews/${encodeURIComponent(id)}`, ws(workspaceId));
export type MonitorUpdate = { name?: string; keywords?: string[]; source_ids?: string[]; interval_seconds?: number; timezone?: string; budget_micro_per_window?: number; notification_target_ids?: string[]; enabled?: boolean; expected_version?: number };
export const updateMonitor = (workspaceId: string, id: string, input: MonitorUpdate, confirmExpansion?: string) =>
    http.patch<{ monitor: IntelMonitor; expansion: string[]; version: number }>(
        `/intelligence/monitors/${encodeURIComponent(id)}`,
        { ...input, ...(confirmExpansion ? { confirmExpansion: true } : {}) },
        {
            ...ws(workspaceId),
            headers: { "X-Workspace-Id": workspaceId, ...(confirmExpansion ? { "X-Idempotency-Key": confirmExpansion } : {}) },
        },
    );

function fileNameFrom(headers: unknown, fallback: string) {
    const h = headers as Record<string, string | undefined> | undefined;
    const cd = h?.["content-disposition"] ?? "";
    const star = /filename\*=UTF-8''([^;]+)/i.exec(cd);
    if (star) return decodeURIComponent(star[1]);
    return /filename="([^"]+)"/i.exec(cd)?.[1] ?? fallback;
}
async function saveBlob(url: string, workspaceId: string, params: Record<string, string>, fallback: string) {
    const res = await http.raw<Blob>({ method: "get", url, params, headers: { "X-Workspace-Id": workspaceId }, responseType: "blob" });
    const name = fileNameFrom(res.headers, fallback);
    const href = URL.createObjectURL(res.data);
    const a = document.createElement("a");
    a.href = href;
    a.download = name;
    a.click();
    URL.revokeObjectURL(href);
    return name;
}
/** PRD §5.2 report export (scoped; foreign ids are 404). */
export const exportReport = (workspaceId: string, id: string, format: "md" | "html" | "json") => saveBlob(`/intelligence/reports/${encodeURIComponent(id)}/export`, workspaceId, { format }, `report.${format}`);
/** PRD §10.4 verified media download (the server checks scope + reference, then proxies the private object). */
export const downloadMedia = (workspaceId: string, documentId: string, objectKey: string) => saveBlob("/intelligence/media", workspaceId, { document_id: documentId, object_key: objectKey }, "attachment");
