import { useEffect, useRef, useState } from "react";
import { Alert, App, Button, Card, Input, Select, Space, Tag } from "antd";
import { Link, useSearchParams } from "react-router";
import { agricultureAPI, assignableAgricultureRoles, canChangeAgricultureMemberRoles, type AgricultureMember } from "@/services/api/agriculture";
import { useUserStore } from "@/stores/use-user-store";
import { OrganizationController, type OrganizationState } from "./organization-controller";
import { ConfirmationScope } from './confirmation-scope';

const labels: Record<string, string> = { viewer: "只读客户", analyst: "GIS分析员", editor: "地块编辑员", field_operator: "巡查员", reviewer: "审核员", farm_manager: "农场经理", tenant_admin: "租户管理员", billing_admin: "财务管理员" };
const roleLabel = (role: string) => labels[role] ?? `未识别角色（${role}）`;

function MemberRow({ member, disabled, change }: { member: AgricultureMember; disabled: boolean; change: (member: AgricultureMember, roles: string[]) => void }) {
    const [draft, setDraft] = useState(member.roles);
    useEffect(() => setDraft(member.roles), [member]);
    return <Space wrap>
        <strong>{member.name || member.userId}</strong><Tag>{member.status}</Tag>
        {member.isOwner && <Tag>组织所有者（身份与状态受保护）</Tag>}
        {member.canChangeRoles && <>
            <span data-testid="org-member-roles" data-current-roles={member.roles.join(",")} data-draft-roles={draft.join(",")}><Select aria-label={`${member.name || member.userId}的角色`} mode="multiple" style={{ minWidth: "16rem" }} value={draft} disabled={disabled} onChange={setDraft} options={assignableAgricultureRoles(member.assignableRoles).map(role => ({ value: role, label: <span data-testid={`org-member-role-option-${role}`}>{roleLabel(role)}</span> }))} /></span>
            <Button data-testid="org-member-roles-submit" disabled={disabled || !canChangeAgricultureMemberRoles(member, draft) || draft.join() === member.roles.join()} onClick={() => change(member, draft)}>确认修改角色</Button>
        </>}
        <span>当前：{member.roles.map(roleLabel).join("、") || "无业务角色"}</span>
    </Space>;
}

export default function OrganizationPage() {
    const userId = useUserStore(state => state.user?.id);
    const [params] = useSearchParams();
    const preferred = params.get("workspace_id") ?? "";
    const { modal } = App.useApp();
    const confirmations = useRef(new ConfirmationScope());
    const [controller, setController] = useState<OrganizationController | null>(null);
    const [state, setState] = useState<OrganizationState | null>(null);
    const [name, setName] = useState("");
    const [token, setToken] = useState("");
    const [email, setEmail] = useState("");
    const [role, setRole] = useState<string>();
    useEffect(() => {
        const current = new OrganizationController();
        current.onChange = setState; setController(current); setState(current.state);
        setToken(""); setName(""); setEmail(""); setRole(undefined);
        void current.refresh(preferred);
        return () => {
            current.dispose();
            confirmations.current.clear();
        };
    }, [userId, preferred]);
    const selected = state?.workspaces.find(w => w.id === state.selectedId);
    const disabled = !controller || !state || state.loading || state.busy;
    const confirm = (title: string, detail: string, action: () => Promise<unknown>) => {
        let forget = () => {};
        const handle = modal.confirm({ title, content: detail, okText: "确认执行", cancelText: "取消", okButtonProps: { ...{ "data-testid": "org-confirm" } }, cancelButtonProps: { ...{ "data-testid": "org-cancel" } }, onOk: action, afterClose: () => { forget(); } });
        forget = confirmations.current.add(() => {
            // Clear protected preview before the portal's close animation ends.
            handle.update({ title: null, content: null, footer: null });
            handle.destroy();
        });
    };
    const memberRoles = (member: AgricultureMember, roles: string[]) => {
        if (!controller || !selected) return;
        const id = selected.id;
        confirm("修改成员角色？", `${selected.tenantName} / ${selected.name}：${member.name || member.userId}；${member.roles.map(roleLabel).join("、") || "无业务角色"} → ${roles.map(roleLabel).join("、") || "撤销全部业务角色"}。${member.isOwner ? "只调整自己的业务角色，不改变所有者身份或组织治理权。" : "治理角色会影响组织成员管理；"}农业内容仍需独立资源授权。`, () => controller.changeRoles(id, member.userId, roles));
    };
    return <main className="agri-profile" data-testid="organization-management">
        <header><h1>组织与成员</h1><Link to="/">返回平台工作区</Link></header>
        <p>使用当前平台账号。这里只管理组织关系与角色，不授予未经验证的农场内容访问权。</p>
        {state?.error && <Alert type="error" role="alert" title="操作未完成" description={state.error} action={<Button disabled={state.busy} onClick={() => void controller?.refresh()}>重新读取权限</Button>} />}
        {state?.notice && <Alert type="success" title={state.notice} />}
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
            <Card title="创建组织">
                <form onSubmit={event => { event.preventDefault(); if (!disabled && name.trim()) void controller?.create(name); }}>
                    <Space wrap><Input data-testid="org-create-name" aria-label="组织名称" value={name} onChange={event => setName(event.target.value)} maxLength={120} disabled={disabled} required placeholder="企业或合作社名称" /><Button data-testid="org-create-submit" htmlType="submit" type="primary" disabled={disabled || !name.trim()}>创建组织</Button></Space>
                    <p>失败后重试同一名称会复用本次幂等键，避免重复创建。不会自动购买套餐。</p>
                </form>
            </Card>
            <Card title="接受组织邀请">
                <form onSubmit={event => { event.preventDefault(); if (!disabled && token.trim()) { const value = token; setToken(""); void controller?.accept(value); } }}>
                    <Space wrap><Input.Password data-testid="org-accept-token" aria-label="邀请令" value={token} onChange={event => setToken(event.target.value)} autoComplete="off" disabled={disabled} required placeholder="粘贴管理员提供的一次性邀请令" /><Button data-testid="org-accept-submit" htmlType="submit" disabled={disabled || !token.trim()}>接受邀请</Button></Space>
                </form>
                <p>邀请仅按服务器确认的收件账号及角色接受；不会读取 URL 中的邀请令。</p>
            </Card>
            <Card title="农业组织工作区">
                <span data-testid="org-workspace-select"><Select aria-label="管理工作区" style={{ minWidth: "18rem" }} value={state?.selectedId || undefined} disabled={disabled} placeholder={state?.loading ? "正在读取授权工作区…" : "选择服务器授权的工作区"} onChange={id => { setRole(undefined); setEmail(""); void controller?.refresh(id); }} options={state?.workspaces.map(w => ({ value: w.id, label: `${w.tenantName} / ${w.name}${w.tenantStatus === "disabled" ? "（组织已停用）" : ""}` }))} /></span>
                {state?.loading && <p role="status">正在重新验证组织与成员权限…</p>}
                {!state?.loading && !state?.workspaces.length && <p>当前没有可管理的农业组织工作区。</p>}
                {selected && <>
                    <p>组织状态：<Tag>{selected.tenantStatus}</Tag> 当前角色：{selected.roles.map(roleLabel).join("、") || "无业务角色"}</p>
                    {selected.tenantStatus === "active" && <a href={`/agri/?workspace_id=${encodeURIComponent(selected.id)}`}>进入农业工作台</a>}
                    {selected.canManageOrganization && <Button data-testid="org-status-toggle" danger={selected.tenantStatus === "active"} disabled={disabled} onClick={() => {
                        const status = selected.tenantStatus === "active" ? "disabled" : "active";
                        confirm(status === "disabled" ? "停用整个组织？" : "恢复组织？", `${selected.tenantName}：${status === "disabled" ? "成员后续业务请求将被拒绝，仅所有者可恢复治理入口。" : "恢复成员后续访问仍需有效成员关系及资源授权。"}`, () => controller!.mutate(() => agricultureAPI.organization(selected.id, status), "组织状态已更新并重新验证。"));
                    }}>{selected.tenantStatus === "active" ? "停用组织" : "恢复组织"}</Button>}
                </>}
            </Card>
            {selected?.canManageMembers && selected.tenantStatus === "active" && state?.members && <>
                <Card title="成员与角色">
                    <p>租户管理员可管理组织成员；财务角色不会自动获得农业内容权限。所有者可按服务器授权调整自己的业务角色，但所有者身份及状态不可在此更改。</p>
                    <ul>{state.members.members.map(member => <li data-user-id={member.userId} key={member.userId} style={{ marginBlock: "var(--spacing-4, 1rem)" }}>
                        <MemberRow member={member} disabled={disabled} change={memberRoles} />
                        {!member.isOwner && <Space wrap>
                            {member.status !== "removed" && <Button data-testid="org-member-status" disabled={disabled} onClick={() => {
                                const status = member.status === "active" ? "suspended" : "active";
                                confirm(status === "active" ? "恢复成员？" : "停用成员？", `${selected.tenantName}：${member.name || member.userId}；新状态 ${status}。服务器将重新验证您的治理权限。`, () => controller!.mutate(() => agricultureAPI.member(selected.id, member.userId, { status }), "成员状态已更新并重新验证。"));
                            }}>{member.status === "active" ? "停用" : "恢复"}</Button>}
                            {member.status !== "removed" && <Button data-testid="org-member-remove" danger disabled={disabled} onClick={() => confirm("从组织移除成员？", `${selected.tenantName}：${member.name || member.userId}。其既有会话不会因此获得原组织权限。`, () => controller!.mutate(() => agricultureAPI.member(selected.id, member.userId, { status: "removed" }), "成员已移除并重新验证。"))}>移除</Button>}
                        </Space>}
                    </li>)}</ul>
                </Card>
                <Card title="创建邀请（不自动发送邮件）">
                    <form onSubmit={event => { event.preventDefault(); if (role && !disabled) void controller?.invite(email, role); }}><Space wrap>
                        <Input data-testid="org-invite-email" type="email" aria-label="受邀邮箱" required value={email} onChange={event => setEmail(event.target.value)} disabled={disabled} placeholder="受邀用户已验证的邮箱" />
                        <span data-testid="org-invite-role" data-current-roles={role ?? ""}><Select aria-label="邀请角色" style={{ minWidth: "12rem" }} value={role} onChange={setRole} disabled={disabled} options={assignableAgricultureRoles(state.members.assignableRoles).map(value => ({ value, label: <span data-testid={`org-invite-role-option-${value}`}>{roleLabel(value)}</span> }))} /></span>
                        <Button data-testid="org-invite-create" htmlType="submit" disabled={disabled || !role || !email.trim()}>创建一次性邀请</Button>
                    </Space></form>
                    {state.invitation && <section aria-label="一次性邀请令"><p>仅本次显示，请通过获准渠道自行交给收件人。切换工作区、重新验证或离开页面后清除。</p><Input.TextArea data-testid="org-invite-token" readOnly aria-label="新邀请令" value={state.invitation.token} autoComplete="off" /><p>角色：{roleLabel(state.invitation.invite.role)}；有效期：{state.invitation.invite.expiresAt}</p><Button data-testid="org-invite-revoke" danger disabled={disabled} onClick={() => {
                        const invitation = state.invitation!;
                        confirm("撤销邀请？", `将撤销刚创建的 ${roleLabel(invitation.invite.role)} 邀请。`, () => controller!.mutate(() => agricultureAPI.revoke(selected.id, invitation.invite.id), "邀请已撤销。"));
                    }}>撤销此邀请</Button></section>}
                </Card>
            </>}
        </Space>
    </main>;
}
