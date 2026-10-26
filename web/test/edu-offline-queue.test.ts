import { describe, expect, test } from "bun:test";

import { clearOtherUsers, enqueue, flushQueue, readQueue, type PendingAttempt } from "../src/lib/edu-offline-queue";

class MemStore {
    m = new Map<string, string>();
    get length() {
        return this.m.size;
    }
    key(i: number) {
        return [...this.m.keys()][i] ?? null;
    }
    getItem(k: string) {
        return this.m.get(k) ?? null;
    }
    setItem(k: string, v: string) {
        this.m.set(k, v);
    }
    removeItem(k: string) {
        this.m.delete(k);
    }
}

const item = (clientKey: string): PendingAttempt => ({ assignmentId: "as1", tenant: "t1", item: "main", kind: "answer", response: "2", clientKey, queuedAt: "2026-10-26T00:00:00Z" });

describe("edu offline queue", () => {
    test("dedupes by clientKey and isolates users", () => {
        const s = new MemStore();
        enqueue(s, "u1", item("k1234567"));
        enqueue(s, "u1", item("k1234567"));
        enqueue(s, "u2", item("k7654321"));
        expect(readQueue(s, "u1")).toHaveLength(1);
        expect(readQueue(s, "u2")).toHaveLength(1);
        expect(clearOtherUsers(s, "u1")).toBe(1);
        expect(readQueue(s, "u2")).toHaveLength(0);
        expect(clearOtherUsers(s, null)).toBe(1);
        expect(readQueue(s, "u1")).toHaveLength(0);
    });

    test("flush stops on network failure and keeps the clientKey for replay", async () => {
        const s = new MemStore();
        enqueue(s, "u1", item("aaaaaaaa"));
        enqueue(s, "u1", item("bbbbbbbb"));
        const seen: string[] = [];
        let online = false;
        const send = async (p: PendingAttempt) => {
            seen.push(p.clientKey);
            return online ? { ok: true } : { ok: false, offline: true };
        };
        let r = await flushQueue(s, "u1", send);
        expect(r.remaining.map((x) => x.clientKey)).toEqual(["aaaaaaaa", "bbbbbbbb"]);
        online = true;
        r = await flushQueue(s, "u1", send);
        expect(r.sent).toHaveLength(2);
        expect(readQueue(s, "u1")).toHaveLength(0);
        expect(seen).toEqual(["aaaaaaaa", "aaaaaaaa", "bbbbbbbb"]);
    });

    test("a business rejection is dequeued, not retried forever", async () => {
        const s = new MemStore();
        enqueue(s, "u1", item("cccccccc"));
        const r = await flushQueue(s, "u1", async () => ({ ok: false, code: "ASSIGNMENT_CLOSED" }));
        expect(r.sent[0]).toMatchObject({ ok: false, code: "ASSIGNMENT_CLOSED" });
        expect(readQueue(s, "u1")).toHaveLength(0);
    });
});
