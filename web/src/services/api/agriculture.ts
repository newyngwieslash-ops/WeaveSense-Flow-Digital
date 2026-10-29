import { http } from "./request";

export const agricultureRoles = ["viewer", "analyst", "editor", "field_operator", "reviewer", "farm_manager", "tenant_admin", "billing_admin"] as const;
export type AgricultureRole = typeof agricultureRoles[number];
export type MemberStatus = "active" | "suspended" | "removed";
export interface AgricultureWorkspace {
    id: string; tenantId: string; name: string; slug: string; tenantName: string;
    tenantStatus: "active" | "disabled"; roles: string[];
    canManageMembers: boolean; canManageOrganization: boolean;
}
export interface AgricultureMember { userId: string; name: string; roles: string[]; status: MemberStatus; isOwner: boolean; canChangeRoles: boolean; assignableRoles: string[] }
export interface AgricultureMembers { members: AgricultureMember[]; assignableRoles: string[] }
export interface AgricultureInvite { invite: { id: string; email: string; role: string; expiresAt: string }; token: string }
export type AgricultureMemberChange = { roles: string[]; status?: never } | { status: MemberStatus; roles?: never };
export function validateAgricultureWorkspaces(value: unknown): AgricultureWorkspace[] {
    if (!Array.isArray(value)) throw new Error("农业工作区返回格式异常。");
    const seen = new Set<string>();
    for (const row of value) {
        if (!row || typeof row !== "object" || ![row.id, row.tenantId].every(id => typeof id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(id))
            || ![row.name, row.slug, row.tenantName].every(text => typeof text === "string" && text.length > 0 && text.length <= 256)
            || !["active", "disabled"].includes(row.tenantStatus) || typeof row.canManageMembers !== "boolean" || typeof row.canManageOrganization !== "boolean"
            || !Array.isArray(row.roles) || row.roles.some((role: unknown) => !agricultureRoles.includes(role as AgricultureRole)) || new Set(row.roles).size !== row.roles.length || seen.has(row.id)) throw new Error("农业工作区返回格式异常。");
        seen.add(row.id);
    }
    return value;
}
const workspacePath = (id: string) => `/agriculture/workspaces/${encodeURIComponent(id)}`;
export const agricultureAPI = {
    workspaces: (signal?: AbortSignal) => http.get<unknown>("/agriculture/workspaces", { signal }).then(validateAgricultureWorkspaces),
    create: (name: string, key: string) => http.post<{ tenant: { id: string; name: string; status: string }; workspace: AgricultureWorkspace }>("/agriculture/organizations", { name }, { headers: { "X-Idempotency-Key": key } }),
    members: (id: string, signal?: AbortSignal) => http.get<AgricultureMembers>(`${workspacePath(id)}/members`, { signal }),
    invite: (id: string, email: string, role: string) => http.post<AgricultureInvite>(`${workspacePath(id)}/invites`, { email, role }),
    accept: (token: string) => http.post<{ workspaceId: string; tenantId: string; roles: string[] }>("/agriculture/invites/accept", { token }),
    member: (id: string, userId: string, change: AgricultureMemberChange) => http.patch<{ ok: boolean }>(`${workspacePath(id)}/members/${encodeURIComponent(userId)}`, change),
    organization: (id: string, status: "active" | "disabled") => http.patch<{ ok: boolean }>(`${workspacePath(id)}/organization`, { status }),
    revoke: (id: string, inviteId: string) => http.post<{ ok: boolean }>(`${workspacePath(id)}/invites/${encodeURIComponent(inviteId)}/revoke`, {}),
};

export function assignableAgricultureRoles(values: string[]): AgricultureRole[] {
    return agricultureRoles.filter(role => values.includes(role));
}

export function canChangeAgricultureMemberRoles(member: AgricultureMember, roles: string[]): boolean {
    return member.canChangeRoles === true && (roles.length > 0 || member.isOwner)
        && new Set(roles).size === roles.length
        && roles.every(role => assignableAgricultureRoles(member.assignableRoles).includes(role as AgricultureRole));
}
