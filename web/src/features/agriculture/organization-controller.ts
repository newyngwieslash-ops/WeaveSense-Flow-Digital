import { agricultureAPI, type AgricultureInvite, type AgricultureMembers, type AgricultureWorkspace } from "@/services/api/agriculture";

export interface OrganizationState {
    workspaces: AgricultureWorkspace[]; selectedId: string; members: AgricultureMembers | null;
    loading: boolean; busy: boolean; error: string; notice: string; invitation: AgricultureInvite | null;
}
export class OrganizationController {
    state: OrganizationState = { workspaces: [], selectedId: "", members: null, loading: false, busy: false, error: "", notice: "", invitation: null };
    onChange = (_: OrganizationState) => {};
    private generation = 0;
    private disposed = false;
    private request?: AbortController;
    private createAttempt?: { name: string; key: string };
    constructor(private api = agricultureAPI, private key = () => crypto.randomUUID()) {}
    private update(change: Partial<OrganizationState>) { if (!this.disposed) { this.state = { ...this.state, ...change }; this.onChange(this.state); } }
    dispose() { this.disposed = true; this.generation++; this.request?.abort(); this.state = { ...this.state, invitation: null, members: null, workspaces: [] }; }
    async refresh(preferred = this.state.selectedId) {
        if (this.disposed) return;
        const generation = ++this.generation;
        this.request?.abort(); const request = this.request = new AbortController();
        this.update({ loading: true, workspaces: [], members: null, invitation: null, error: "", selectedId: "" });
        try {
            const workspaces = await this.api.workspaces(request.signal);
            if (generation !== this.generation || this.disposed) return;
            const selected = workspaces.find(w => w.id === preferred) ?? (preferred ? undefined : workspaces[0]);
            this.update({ workspaces, selectedId: selected?.id ?? "" });
            if (preferred && !selected) this.update({ error: "所选工作区不在当前服务器授权列表中。" });
            if (selected?.canManageMembers && selected.tenantStatus === "active") {
                const members = await this.api.members(selected.id, request.signal);
                if (generation !== this.generation || this.disposed) return;
                this.update({ members });
            }
        } catch (error) {
            if (generation === this.generation && !this.disposed) this.update({ members: null, workspaces: [], selectedId: "", error: error instanceof Error ? error.message : "读取失败，请重试。" });
        } finally { if (generation === this.generation) this.update({ loading: false }); }
    }
    async mutate(action: () => Promise<unknown>, success: string, preferred?: string) {
        if (this.disposed || this.state.busy || this.state.loading) return false;
        const generation = this.generation;
        this.update({ busy: true, error: "", notice: "", invitation: null });
        try {
            const result = await action();
            if (this.disposed || generation !== this.generation) return false;
            await this.refresh(preferred ?? (typeof result === "string" ? result : this.state.selectedId));
            if (this.generation !== generation + 1 || this.disposed) return false;
            if (!this.state.error) this.update({ notice: success });
            return true;
        } catch (error) {
            if (generation === this.generation) this.update({ members: null, workspaces: [], selectedId: "", error: error instanceof Error ? error.message : "操作失败，尚未确认完成。" });
            return false;
        } finally { this.update({ busy: false }); }
    }
    create(name: string) {
        const normalized = name.trim();
        if (this.createAttempt?.name !== normalized) this.createAttempt = { name: normalized, key: this.key() };
        const attempt = this.createAttempt;
        return this.mutate(async () => (await this.api.create(attempt.name, attempt.key)).workspace.id, "组织已创建，并已重新读取权限。");
    }
    accept(token: string) { return this.mutate(async () => (await this.api.accept(token.trim())).workspaceId, "邀请已接受，并已重新读取权限。"); }
    async invite(email: string, role: string) {
        const workspace = this.state.workspaces.find(w => w.id === this.state.selectedId);
        if (!workspace?.canManageMembers || !this.state.members?.assignableRoles.includes(role) || this.state.busy || this.state.loading || this.disposed) return;
        const generation = this.generation;
        this.update({ busy: true, invitation: null, error: "", notice: "" });
        try { const invitation = await this.api.invite(workspace.id, email.trim(), role); if (generation === this.generation) this.update({ invitation }); }
        catch (error) { if (generation === this.generation) this.update({ members: null, workspaces: [], selectedId: "", error: error instanceof Error ? error.message : "邀请创建失败。" }); }
        finally { this.update({ busy: false }); }
    }
}
