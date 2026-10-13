import { localForageStorageForScope } from "@/lib/localforage-storage";
import { markdownPlainText } from "@/lib/markdown-plain-text";
import { getActiveUserScope } from "@/lib/user-scope";
import type { AgentPermissionMode, AgentRun } from "@/services/api/agent";
import { http } from "@/services/api/request";

export type CloudAgentMessagePresentation = {
    displayText?: string;
    canvasReferenceNodeId?: string;
};

export type CloudAgentConversationMessage = {
    id: string;
    role: "user" | "assistant" | "system" | "tool" | "error";
    title?: string;
    text: string;
    streaming?: boolean;
    reasoning?: boolean;
    planItems?: Array<{ id: string; title: string; status: "pending" | "doing" | "done" }>;
    planTerminal?: boolean;
    question?: { question: string; options: Array<{ label: string; detail?: string }>; allowFreeform?: boolean };
    meta?: string;
    detail?: unknown;
    attachments?: Array<{ id: string; name: string; url: string }>;
    interjection?: "sent" | "undelivered";
};

export type CloudAgentConversation = {
    id: string;
    title: string;
    messages: CloudAgentConversationMessage[];
    run: AgentRun | null;
    model?: string;
    permissionMode: AgentPermissionMode;
    skillIds?: string[];
    /** Tombstones of deleted messages (either side); a stale copy never resurrects them. */
    deletedIds?: string[];
    createdAt: string;
    updatedAt: string;
};

type CloudAgentConversationDocument = {
    version: 1;
    activeId: string | null;
    conversations: CloudAgentConversation[];
};

export type CloudAgentPendingSubmission = CloudAgentMessagePresentation & {
    fingerprint: string;
    key: string;
    request?: import("@/services/api/agent").CreateAgentRunInput;
    parentRunId?: string;
    messageId?: string;
};

const CLOUD_AGENT_CONVERSATIONS_KEY = "cloud-agent-conversations-v1";

// Server copy (cross-device). The local store stays as a warm cache / offline fallback.
const serverVersions = new Map<string, number>();
type ServerDoc = { version: number; document: CloudAgentConversationDocument | null };
const serverPath = (canvasId: string) => `/agent/canvas-conversations/${encodeURIComponent(canvasId)}`;

export async function loadCloudAgentConversations(canvasId: string): Promise<CloudAgentConversationDocument> {
    if (!canvasId) throw new Error("缺少画布 ID，无法读取 Agent 对话");
    try {
        const remote = await http.get<ServerDoc>(serverPath(canvasId));
        serverVersions.set(canvasId, remote.version);
        if (remote.document && isConversationDocument(remote.document)) return remote.document;
    } catch {
        // offline or server unavailable: fall back to the local cache below
    }
    return loadLocalCloudAgentConversations(canvasId);
}

async function loadLocalCloudAgentConversations(canvasId: string): Promise<CloudAgentConversationDocument> {
    const value = await localForageStorageForScope(getActiveUserScope()).getItem(storageKey(canvasId));
    if (!value) return emptyDocument();

    let parsed: unknown;
    try {
        parsed = JSON.parse(value);
    } catch {
        throw new Error("Agent 对话历史已损坏");
    }
    if (!isConversationDocument(parsed)) throw new Error("Agent 对话历史格式无效");
    return parsed;
}

export async function saveCloudAgentConversations(canvasId: string, activeId: string | null, conversations: CloudAgentConversation[]) {
    if (!canvasId) throw new Error("缺少画布 ID，无法保存 Agent 对话");
    // 流式标记只属于内存中的观察状态。若网络在最后一个 delta 后断开，
    // 也不能让下次恢复把思考卡片误判为仍在实时展开。
    const persistedConversations = conversations.map((conversation) => ({
        ...conversation,
        messages: conversation.messages.map((message) => ({ ...message, streaming: false })),
    }));
    const document: CloudAgentConversationDocument = { version: 1, activeId, conversations: persistedConversations };
    await localForageStorageForScope(getActiveUserScope()).setItem(storageKey(canvasId), JSON.stringify(document));
    await pushServerDocument(canvasId, document);
}

async function pushServerDocument(canvasId: string, document: CloudAgentConversationDocument, retry = true): Promise<void> {
    try {
        const saved = await http.put<{ version: number }>(serverPath(canvasId), { baseVersion: serverVersions.get(canvasId) ?? 0, document });
        serverVersions.set(canvasId, saved.version);
    } catch (cause) {
        if (retry && (cause as { status?: number })?.status === 409) {
            // Another device wrote first: merge by conversation id (newest updatedAt wins), then retry once.
            try {
                const remote = await http.get<ServerDoc>(serverPath(canvasId));
                serverVersions.set(canvasId, remote.version);
                return pushServerDocument(canvasId, mergeDocuments(remote.document, document), false);
            } catch {
                return;
            }
        }
        // Network failure: the local copy is kept and the next save retries.
    }
}

/**
 * Conflict merge for concurrent writers (another device, or turns added from the
 * intelligence page, which the server writes back as `dir_*` messages). Per
 * conversation the newer copy wins for metadata, and messages are the union by
 * id: messages only the other side has are kept in their original order.
 */
export function mergeDocuments(server: CloudAgentConversationDocument | null, local: CloudAgentConversationDocument): CloudAgentConversationDocument {
    if (!server || !isConversationDocument(server)) return local;
    const byId = new Map(server.conversations.map((c) => [c.id, c]));
    for (const c of local.conversations) {
        const other = byId.get(c.id);
        if (!other) {
            byId.set(c.id, c);
            continue;
        }
        const deleted = new Set([...(other.deletedIds || []), ...(c.deletedIds || [])]);
        const [base, extra] = other.updatedAt <= c.updatedAt ? [c, other] : [other, c];
        const seen = new Set(base.messages.map((m) => m.id));
        const messages = [...base.messages];
        extra.messages.forEach((m, index) => {
            if (seen.has(m.id)) return;
            seen.add(m.id);
            // Insert after the closest preceding message both sides share.
            let at = messages.length;
            for (let i = index - 1; i >= 0; i--) {
                const pos = messages.findIndex((x) => x.id === extra.messages[i].id);
                if (pos >= 0) {
                    at = pos + 1;
                    break;
                }
            }
            messages.splice(at, 0, m);
        });
        const merged = { ...base, messages: messages.filter((m) => !deleted.has(m.id)) };
        if (deleted.size) merged.deletedIds = [...deleted].sort();
        byId.set(c.id, merged);
    }
    const conversations = [...byId.values()].sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1));
    return { version: 1, activeId: local.activeId, conversations };
}

/** Delete a sidebar message: removed locally and tombstoned so the server and other replicas drop it too. */
export function deleteConversationMessage(conversation: CloudAgentConversation, messageId: string): CloudAgentConversation {
    const deletedIds = [...new Set([...(conversation.deletedIds || []), messageId])].sort();
    return { ...conversation, messages: conversation.messages.filter((m) => m.id !== messageId), deletedIds, updatedAt: new Date().toISOString() };
}

/** Edit a sidebar message's text (newer conversation copy wins in mergeDocuments). */
export function editConversationMessage(conversation: CloudAgentConversation, messageId: string, text: string): CloudAgentConversation {
    return { ...conversation, messages: conversation.messages.map((m) => (m.id === messageId ? { ...m, text } : m)), updatedAt: new Date().toISOString() };
}

// Keep the retry key outside the chat snapshot. If the response is lost, a
// page refresh can safely repeat the same request without creating another
// billed Agent turn.
export async function loadCloudAgentPendingSubmission(canvasId: string, conversationId: string) {
    if (!canvasId || !conversationId) return null;
    const value = await localForageStorageForScope(getActiveUserScope()).getItem(pendingStorageKey(canvasId, conversationId));
    if (value === null || value === undefined) return null;
    // A corrupt recovery record is not proof that the previous POST never ran.
    // Fail closed rather than discarding its identity and risking another charge.
    try {
        const parsed = JSON.parse(value) as Partial<CloudAgentPendingSubmission> | null;
        if (!parsed || typeof parsed.fingerprint !== "string" || typeof parsed.key !== "string" || parsed.key.length < 8) throw new Error("invalid identity");
        if (parsed.request !== undefined && (!parsed.request || parsed.request.idempotencyKey !== parsed.key || parsed.request.canvasId !== canvasId || typeof parsed.request.prompt !== "string")) throw new Error("invalid request");
        if (parsed.parentRunId !== undefined && typeof parsed.parentRunId !== "string") throw new Error("invalid parent");
        if (parsed.messageId !== undefined && typeof parsed.messageId !== "string") throw new Error("invalid message");
        return {
            fingerprint: parsed.fingerprint,
            key: parsed.key,
            request: parsed.request,
            parentRunId: parsed.parentRunId,
            messageId: parsed.messageId,
            displayText: parsed.displayText,
            canvasReferenceNodeId: parsed.canvasReferenceNodeId,
        } satisfies CloudAgentPendingSubmission;
    } catch {
        throw new Error("待确认请求记录损坏，已暂停本对话发送；请先在任务中心核对原运行，不要直接重复生成");
    }
}

export async function saveCloudAgentPendingSubmission(canvasId: string, conversationId: string, pending: CloudAgentPendingSubmission) {
    if (!canvasId || !conversationId) throw new Error("缺少 Agent 对话范围，无法保存幂等提交");
    await localForageStorageForScope(getActiveUserScope()).setItem(pendingStorageKey(canvasId, conversationId), JSON.stringify(pending));
}

export async function clearCloudAgentPendingSubmission(canvasId: string, conversationId: string) {
    if (!canvasId || !conversationId) return;
    await localForageStorageForScope(getActiveUserScope()).removeItem(pendingStorageKey(canvasId, conversationId));
}

export function cloudAgentConversationTitle(messages: CloudAgentConversationMessage[]) {
    const firstPrompt = displayConversationTitle(messages.find((message) => message.role === "user")?.text || "");
    return firstPrompt.length > 28 ? `${firstPrompt.slice(0, 28)}...` : firstPrompt;
}

function displayConversationTitle(value: string) {
    return markdownPlainText(value.replace(/@\[skill:[^\]]+\]/gu, "技能包")) || "新对话";
}

function storageKey(canvasId: string) {
    return `${CLOUD_AGENT_CONVERSATIONS_KEY}:${encodeURIComponent(canvasId)}`;
}

function pendingStorageKey(canvasId: string, conversationId: string) {
    return `${CLOUD_AGENT_CONVERSATIONS_KEY}:pending:${encodeURIComponent(canvasId)}:${encodeURIComponent(conversationId)}`;
}

function emptyDocument(): CloudAgentConversationDocument {
    return { version: 1, activeId: null, conversations: [] };
}

function isConversationDocument(value: unknown): value is CloudAgentConversationDocument {
    if (!value || typeof value !== "object") return false;
    const document = value as Partial<CloudAgentConversationDocument>;
    if (document.version !== 1 || !Array.isArray(document.conversations)) return false;
    return document.conversations.every((conversation) => {
        if (!conversation || typeof conversation !== "object") return false;
        const candidate = conversation as Partial<CloudAgentConversation>;
        return (
            typeof candidate.id === "string" &&
            typeof candidate.title === "string" &&
            Array.isArray(candidate.messages) &&
            typeof candidate.createdAt === "string" &&
            typeof candidate.updatedAt === "string" &&
            ["read_only", "auto", "request_approval"].includes(candidate.permissionMode || "")
        );
    });
}
