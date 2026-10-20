import { useCallback, useEffect, useState } from "react";
import { Button, Card, Input, List, Select, Space, Tag, Typography, message } from "antd";
import { StatusBadge } from "@/components/ui/base/badges";

import {
    acceptInvite,
    createInvite,
    createOrganization,
    getBilling,
    installSkills,
    listNotifications,
    listMembers,
    listSkills,
    markNotificationRead,
    resolveReservation,
    setMemberRole,
    type OrgMember,
    type BillingAccount,
    type BudgetReservation,
    type FlowNotification,
    type SkillInstall,
    type SkillPack,
} from "@/services/api/intelligence";

const credits = (micro: number) => (micro / 1_000_000).toFixed(2);
export const RESERVATION_STATUS_LABEL: Record<string, string> = {
    reserved: "已预留",
    settled: "已结算",
    released: "已释放",
    unknown: "结果未决",
};
export const ROLE_LABEL: Record<string, string> = {
    owner: "所有者 (owner)",
    admin: "管理员 (admin)",
    editor: "编辑者 (editor)",
    member: "编辑者 (editor)",
    viewer: "只读 (viewer)",
};

export function normalizeEditableRole(role: OrgMember["role"]): "admin" | "editor" | "viewer" {
    if (role === "admin" || role === "viewer") return role;
    return "editor";
}

export type WorkspaceAdminInitialData = {
    notes?: FlowNotification[];
    packs?: SkillPack[];
    installed?: SkillInstall[];
    account?: BillingAccount;
    reservations?: BudgetReservation[];
    members?: OrgMember[];
    canManage?: boolean;
};

/** P5: notifications, skill pack install, billing/reservations and org invites for the active workspace. */
export function WorkspaceAdminPanel({
    workspaceId,
    onWorkspacesChanged,
    initialData,
}: {
    workspaceId?: string;
    onWorkspacesChanged?: () => void;
    initialData?: WorkspaceAdminInitialData;
}) {
    const [notes, setNotes] = useState<FlowNotification[]>(initialData?.notes ?? []);
    const [packs, setPacks] = useState<SkillPack[]>(initialData?.packs ?? []);
    const [installed, setInstalled] = useState<SkillInstall[]>(initialData?.installed ?? []);
    const [account, setAccount] = useState<BillingAccount | undefined>(initialData?.account);
    const [reservations, setReservations] = useState<BudgetReservation[]>(initialData?.reservations ?? []);
    const [orgName, setOrgName] = useState("");
    const [inviteEmail, setInviteEmail] = useState("");
    const [inviteRole, setInviteRole] = useState("editor");
    const [inviteToken, setInviteToken] = useState("");
    const [acceptToken, setAcceptToken] = useState("");
    const [members, setMembers] = useState<OrgMember[]>(initialData?.members ?? []);
    const [canManage, setCanManage] = useState(initialData?.canManage ?? false);

    const refresh = useCallback(() => {
        if (!workspaceId) return;
        void listNotifications(workspaceId)
            .then(setNotes)
            .catch(() => setNotes([]));
        void listSkills(workspaceId)
            .then((r) => {
                setPacks(r.packs ?? []);
                setInstalled(r.installed ?? []);
            })
            .catch(() => setPacks([]));
        void getBilling(workspaceId)
            .then((r) => {
                setAccount(r.account);
                setReservations(r.reservations ?? []);
            })
            .catch(() => setAccount(undefined));
        void listMembers(workspaceId)
            .then((r) => {
                setMembers(r.members ?? []);
                setCanManage(!!r.canManage);
            })
            .catch(() => setMembers([]));
    }, [workspaceId]);
    useEffect(refresh, [refresh]);

    const unread = notes.filter((n) => !n.readAt).length;
    const run = async (fn: () => Promise<unknown>, ok: string) => {
        try {
            await fn();
            message.success(ok);
            refresh();
        } catch (err) {
            message.error(err instanceof Error ? err.message : String(err));
        }
    };
    if (!workspaceId) return null;
    const installedIds = new Set(installed.map((i) => `${i.skillId}@${i.version}`));

    return (
        <Space direction="vertical" style={{ width: "100%", padding: "0 16px 16px" }}>
            <Card
                size="small"
                title={
                    <Space size={8}>
                        <span>站内通知</span>
                        <StatusBadge tone={unread > 0 ? "warning" : "neutral"} size="sm" label={unread > 0 ? `${unread} 条未读` : "全部已读"} />
                    </Space>
                }
                extra={
                    <Button size="small" disabled={!unread} onClick={() => run(() => markNotificationRead(workspaceId, "all"), "已全部标为已读")}>
                        全部标为已读
                    </Button>
                }
            >
                <List
                    size="small"
                    dataSource={notes.slice(0, 20)}
                    locale={{ emptyText: "暂无通知" }}
                    renderItem={(n) => (
                        <List.Item
                            actions={
                                n.readAt
                                    ? []
                                    : [
                                          <a key="r" onClick={() => run(() => markNotificationRead(workspaceId, n.id), "已标为已读")}>
                                              标为已读
                                          </a>,
                                      ]
                            }
                        >
                            <Space>
                                <Tag color={n.readAt ? "default" : "blue"}>{n.type}</Tag>
                                <Typography.Text strong={!n.readAt}>{n.title}</Typography.Text>
                                <Typography.Text type="secondary">{new Date(n.createdAt).toLocaleString()}</Typography.Text>
                            </Space>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title="技能包">
                <List
                    size="small"
                    dataSource={packs}
                    locale={{ emptyText: "暂无可用技能包" }}
                    renderItem={(p) => (
                        <List.Item
                            actions={[
                                <Button key="i" size="small" onClick={() => run(() => installSkills(workspaceId, { packId: p.id }), `已安装 ${p.name}`)}>
                                    安装技能包
                                </Button>,
                            ]}
                        >
                            <Space wrap>
                                <Typography.Text strong>{p.name}</Typography.Text>
                                <Tag>v{p.version}</Tag>
                                {p.skills.map((s) => (
                                    <Tag key={s.id} color={installedIds.has(`${s.id}@${s.version}`) ? "green" : "default"}>
                                        {s.name}
                                    </Tag>
                                ))}
                            </Space>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title="计费与预算预留">
                {account ? (
                    <Space direction="vertical" style={{ width: "100%" }}>
                        <Space wrap>
                            <Tag color={account.kind === "shared" ? "purple" : "default"}>{account.kind === "shared" ? "团队共享账户" : "个人账户"}</Tag>
                            <span>月度配额：{account.monthlyQuotaMicro ? `${credits(account.monthlyQuotaMicro)} 积分` : "不限额"}</span>
                        </Space>
                        <List
                            size="small"
                            dataSource={reservations.slice(0, 10)}
                            locale={{ emptyText: "暂无预算预留记录" }}
                            renderItem={(r) => (
                                <List.Item>
                                    <Space wrap>
                                        <Tag color={r.status === "unknown" ? "orange" : r.status === "reserved" ? "blue" : "default"}>
                                            {RESERVATION_STATUS_LABEL[r.status] ?? r.status}
                                        </Tag>
                                        <span>{r.purpose}</span>
                                        <span>
                                            预留 {credits(r.reservedMicro)} · 实耗 {credits(r.consumedMicro)}
                                        </span>
                                        {r.status === "unknown" && (
                                            <>
                                                <Button size="small" onClick={() => void run(() => resolveReservation(workspaceId!, r.id, "succeeded"), "已确认扣费")}>
                                                    标记已执行
                                                </Button>
                                                <Button size="small" onClick={() => void run(() => resolveReservation(workspaceId!, r.id, "not_executed"), "已释放预留")}>
                                                    标记未执行
                                                </Button>
                                            </>
                                        )}
                                    </Space>
                                </List.Item>
                            )}
                        />
                    </Space>
                ) : (
                    <Typography.Text type="secondary">暂无计费账户信息</Typography.Text>
                )}
            </Card>
            <Card size="small" title={`工作区成员（${members.length}）`}>
                <List
                    size="small"
                    dataSource={members}
                    locale={{ emptyText: "暂无成员" }}
                    renderItem={(m) => (
                        <List.Item>
                            <Space wrap>
                                <span>{m.name || m.email || m.userId}</span>
                                {m.role === "owner" || !canManage ? (
                                    <Tag>{ROLE_LABEL[m.role] ?? m.role}</Tag>
                                ) : (
                                    <Select
                                        size="small"
                                        style={{ width: 150 }}
                                        value={normalizeEditableRole(m.role)}
                                        onChange={(role: "admin" | "editor" | "viewer") => void run(() => setMemberRole(workspaceId!, m.userId, role), "成员角色已更新")}
                                        options={[
                                            { value: "admin", label: "管理员 (admin)" },
                                            { value: "editor", label: "编辑者 (editor)" },
                                            { value: "viewer", label: "只读 (viewer)" },
                                        ]}
                                    />
                                )}
                            </Space>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title="组织与成员邀请">
                <Space direction="vertical" style={{ width: "100%" }}>
                    <Space.Compact style={{ width: "100%" }}>
                        <Input placeholder="新组织名称" value={orgName} onChange={(e) => setOrgName(e.target.value)} />
                        <Button
                            disabled={!orgName.trim()}
                            onClick={() =>
                                run(async () => {
                                    await createOrganization(orgName.trim());
                                    setOrgName("");
                                    onWorkspacesChanged?.();
                                }, "组织已创建")
                            }
                        >
                            创建组织
                        </Button>
                    </Space.Compact>
                    <Space.Compact style={{ width: "100%" }}>
                        <Input placeholder="受邀人邮箱" value={inviteEmail} onChange={(e) => setInviteEmail(e.target.value)} />
                        <Select
                            value={inviteRole}
                            onChange={setInviteRole}
                            style={{ width: 150 }}
                            options={[
                                { value: "viewer", label: "只读 (viewer)" },
                                { value: "editor", label: "编辑者 (editor)" },
                                { value: "admin", label: "管理员 (admin)" },
                            ]}
                        />
                        <Button
                            disabled={!inviteEmail.trim()}
                            onClick={() =>
                                run(async () => {
                                    const r = await createInvite(workspaceId, inviteEmail.trim(), inviteRole);
                                    setInviteToken(r.token);
                                }, "邀请令已生成")
                            }
                        >
                            生成邀请
                        </Button>
                    </Space.Compact>
                    {inviteToken && <Typography.Text copyable>{inviteToken}</Typography.Text>}
                    <Space.Compact style={{ width: "100%" }}>
                        <Input placeholder="输入邀请令加入工作区" value={acceptToken} onChange={(e) => setAcceptToken(e.target.value)} />
                        <Button
                            disabled={!acceptToken.trim()}
                            onClick={() =>
                                run(async () => {
                                    await acceptInvite(acceptToken.trim());
                                    setAcceptToken("");
                                    onWorkspacesChanged?.();
                                }, "已加入工作区")
                            }
                        >
                            接受邀请
                        </Button>
                    </Space.Compact>
                </Space>
            </Card>
        </Space>
    );
}
