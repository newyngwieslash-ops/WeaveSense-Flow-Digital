/**
 * 学生作答的离线队列。只保存学生自己的输入与 clientKey，不缓存讲解、提示或答案；
 * 按用户隔离，切换账号或退出时整体清除。重放沿用原 clientKey，服务端据此幂等去重，
 * 因此网络恢复后重复提交不会产生第二条记录。
 */
export type PendingAttempt = { assignmentId: string; tenant: string; item: "main" | "check"; kind: "answer" | "no_idea"; response: string; clientKey: string; reviewItemId?: string; queuedAt: string };
type KV = Pick<Storage, "getItem" | "setItem" | "removeItem" | "key" | "length">;

const PREFIX = "edu.pending.v1:";
const MAX_ITEMS = 50;

export function queueKey(userId: string) {
    return `${PREFIX}${userId}`;
}

export function readQueue(store: KV, userId: string): PendingAttempt[] {
    try {
        const v = JSON.parse(store.getItem(queueKey(userId)) || "[]");
        return Array.isArray(v) ? (v as PendingAttempt[]) : [];
    } catch {
        return [];
    }
}

export function enqueue(store: KV, userId: string, item: PendingAttempt): PendingAttempt[] {
    const list = readQueue(store, userId).filter((x) => x.clientKey !== item.clientKey);
    list.push(item);
    const next = list.slice(-MAX_ITEMS);
    store.setItem(queueKey(userId), JSON.stringify(next));
    return next;
}

/** 依次重放；网络失败即停（保留剩余），服务端给出明确结果（成功或业务拒绝）则出队。 */
export async function flushQueue(store: KV, userId: string, send: (p: PendingAttempt) => Promise<{ ok: boolean; offline?: boolean; code?: string }>) {
    const sent: { item: PendingAttempt; ok: boolean; code?: string }[] = [];
    let rest = readQueue(store, userId);
    while (rest.length > 0) {
        const head = rest[0];
        const r = await send(head);
        if (r.offline) break;
        sent.push({ item: head, ok: r.ok, code: r.code });
        rest = rest.slice(1);
        if (rest.length) store.setItem(queueKey(userId), JSON.stringify(rest));
        else store.removeItem(queueKey(userId));
    }
    return { sent, remaining: rest };
}

/** 清除所有非当前用户的队列（userId 为空表示退出登录，全部清除）。 */
export function clearOtherUsers(store: KV, userId: string | null) {
    const drop: string[] = [];
    for (let i = 0; i < store.length; i++) {
        const k = store.key(i);
        if (k && k.startsWith(PREFIX) && k !== (userId ? queueKey(userId) : "")) drop.push(k);
    }
    drop.forEach((k) => store.removeItem(k));
    return drop.length;
}
