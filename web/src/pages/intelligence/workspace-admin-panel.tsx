import { useCallback, useEffect, useState } from "react";
import { Badge, Button, Card, Input, List, Select, Space, Tag, Typography, message } from "antd";

import {
    acceptInvite,
    createInvite,
    createOrganization,
    getBilling,
    installSkills,
    listNotifications,
    listSkills,
    markNotificationRead,
    type BillingAccount,
    type BudgetReservation,
    type FlowNotification,
    type SkillInstall,
    type SkillPack,
} from "@/services/api/intelligence";

const credits = (micro: number) => (micro / 1_000_000).toFixed(2);

/** P5: notifications, skill pack install, billing/reservations and org invites for the active workspace. */
export function WorkspaceAdminPanel({ workspaceId, onWorkspacesChanged }: { workspaceId?: string; onWorkspacesChanged?: () => void }) {
    const [notes, setNotes] = useState<FlowNotification[]>([]);
    const [packs, setPacks] = useState<SkillPack[]>([]);
    const [installed, setInstalled] = useState<SkillInstall[]>([]);
    const [account, setAccount] = useState<BillingAccount>();
    const [reservations, setReservations] = useState<BudgetReservation[]>([]);
    const [orgName, setOrgName] = useState("");
    const [inviteEmail, setInviteEmail] = useState("");
    const [inviteRole, setInviteRole] = useState("editor");
    const [inviteToken, setInviteToken] = useState("");
    const [acceptToken, setAcceptToken] = useState("");

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
        <Space direction="vertical" style={{ width: "100%" }}>
            <Card
                size="small"
                title={
                    <Badge count={unread} offset={[12, 0]}>
                        Notifications
                    </Badge>
                }
                extra={
                    <Button size="small" disabled={!unread} onClick={() => run(() => markNotificationRead(workspaceId, "all"), "All read")}>
                        Mark all read
                    </Button>
                }
            >
                <List
                    size="small"
                    dataSource={notes.slice(0, 20)}
                    locale={{ emptyText: "No notifications" }}
                    renderItem={(n) => (
                        <List.Item
                            actions={
                                n.readAt
                                    ? []
                                    : [
                                          <a key="r" onClick={() => run(() => markNotificationRead(workspaceId, n.id), "Marked read")}>
                                              mark read
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
            <Card size="small" title="Skill packs">
                <List
                    size="small"
                    dataSource={packs}
                    renderItem={(p) => (
                        <List.Item
                            actions={[
                                <Button key="i" size="small" onClick={() => run(() => installSkills(workspaceId, { packId: p.id }), `Installed ${p.name}`)}>
                                    Install pack
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
            <Card size="small" title="Billing">
                {account ? (
                    <Space direction="vertical" style={{ width: "100%" }}>
                        <Space wrap>
                            <Tag color={account.kind === "shared" ? "purple" : "default"}>{account.kind} account</Tag>
                            <span>Monthly quota: {account.monthlyQuotaMicro ? credits(account.monthlyQuotaMicro) : "unlimited"}</span>
                        </Space>
                        <List
                            size="small"
                            dataSource={reservations.slice(0, 10)}
                            locale={{ emptyText: "No reservations" }}
                            renderItem={(r) => (
                                <List.Item>
                                    <Space wrap>
                                        <Tag color={r.status === "unknown" ? "orange" : r.status === "reserved" ? "blue" : "default"}>{r.status}</Tag>
                                        <span>{r.purpose}</span>
                                        <span>
                                            reserved {credits(r.reservedMicro)} · used {credits(r.consumedMicro)}
                                        </span>
                                    </Space>
                                </List.Item>
                            )}
                        />
                    </Space>
                ) : (
                    <Typography.Text type="secondary">Billing unavailable</Typography.Text>
                )}
            </Card>
            <Card size="small" title="Organization & invites">
                <Space direction="vertical" style={{ width: "100%" }}>
                    <Space.Compact style={{ width: "100%" }}>
                        <Input placeholder="New organization name" value={orgName} onChange={(e) => setOrgName(e.target.value)} />
                        <Button
                            disabled={!orgName.trim()}
                            onClick={() =>
                                run(async () => {
                                    await createOrganization(orgName.trim());
                                    setOrgName("");
                                    onWorkspacesChanged?.();
                                }, "Organization created")
                            }
                        >
                            Create
                        </Button>
                    </Space.Compact>
                    <Space.Compact style={{ width: "100%" }}>
                        <Input placeholder="Invitee email" value={inviteEmail} onChange={(e) => setInviteEmail(e.target.value)} />
                        <Select value={inviteRole} onChange={setInviteRole} style={{ width: 110 }} options={["viewer", "editor", "admin"].map((r) => ({ value: r, label: r }))} />
                        <Button
                            disabled={!inviteEmail.trim()}
                            onClick={() =>
                                run(async () => {
                                    const r = await createInvite(workspaceId, inviteEmail.trim(), inviteRole);
                                    setInviteToken(r.token);
                                }, "Invite created")
                            }
                        >
                            Invite
                        </Button>
                    </Space.Compact>
                    {inviteToken && <Typography.Text copyable>{inviteToken}</Typography.Text>}
                    <Space.Compact style={{ width: "100%" }}>
                        <Input placeholder="Invite token" value={acceptToken} onChange={(e) => setAcceptToken(e.target.value)} />
                        <Button
                            disabled={!acceptToken.trim()}
                            onClick={() =>
                                run(async () => {
                                    await acceptInvite(acceptToken.trim());
                                    setAcceptToken("");
                                    onWorkspacesChanged?.();
                                }, "Joined workspace")
                            }
                        >
                            Accept
                        </Button>
                    </Space.Compact>
                </Space>
            </Card>
        </Space>
    );
}
