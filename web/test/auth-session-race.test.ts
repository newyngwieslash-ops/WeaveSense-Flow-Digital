import { afterEach, expect, spyOn, test } from "bun:test";
import { http } from "../src/services/api/request";
import { getAuthSession, invalidateAuthSessionCache, login, logout, register, resetPassword, type AuthSessionPayload, type LocalUser } from "../src/services/api/auth";

function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (reason: unknown) => void;
    const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
}

const user = (id: string): LocalUser => ({ id, username: id, displayName: id, role: "user", status: "active", createdAt: "", updatedAt: "" });
const A: AuthSessionPayload = { user: user("A") };
const B: AuthSessionPayload = { user: user("B") };
let get: ReturnType<typeof spyOn<typeof http, "get">> | undefined;
let post: ReturnType<typeof spyOn<typeof http, "post">> | undefined;

afterEach(() => {
    invalidateAuthSessionCache();
    get?.mockRestore();
    post?.mockRestore();
    get = undefined;
    post = undefined;
});

test("late A after invalidation cannot reach caller or replace cached B", async () => {
    const old = deferred<AuthSessionPayload>();
    const fresh = deferred<AuthSessionPayload>();
    get = spyOn(http, "get").mockReturnValueOnce(old.promise as never).mockReturnValueOnce(fresh.promise as never);
    const oldResult = getAuthSession().catch((error: Error) => error.name);
    invalidateAuthSessionCache();
    const current = getAuthSession();
    fresh.resolve(B);
    expect(await current).toEqual(B);
    old.resolve(A);
    expect(await oldResult).toBe("AbortError");
    expect(await getAuthSession()).toEqual(B);
    expect(get).toHaveBeenCalledTimes(2);
});

test("old finally cannot clear a newer in-flight anonymous check", async () => {
    const old = deferred<AuthSessionPayload>();
    const fresh = deferred<AuthSessionPayload>();
    get = spyOn(http, "get").mockReturnValueOnce(old.promise as never).mockReturnValueOnce(fresh.promise as never);
    const oldResult = getAuthSession().catch((error: Error) => error.name);
    invalidateAuthSessionCache();
    const current = getAuthSession();
    old.resolve(A);
    expect(await oldResult).toBe("AbortError");
    expect(getAuthSession()).toBe(current);
    expect(get).toHaveBeenCalledTimes(2);
    fresh.resolve({ user: null });
    expect(await current).toEqual({ user: null });
    expect(await getAuthSession()).toEqual({ user: null });
});

test("cached hit invalidated before delivery cannot return old identity", async () => {
    get = spyOn(http, "get").mockResolvedValue(A as never);
    await getAuthSession();
    const cached = getAuthSession().catch((error: Error) => error.name);
    invalidateAuthSessionCache();
    expect(await cached).toBe("AbortError");
});

test("invalidation after transport resolution but before final delivery rejects A", async () => {
    const transport = deferred<AuthSessionPayload>();
    get = spyOn(http, "get").mockReturnValueOnce(transport.promise as never);
    const result = getAuthSession().catch((error: Error) => error.name);
    transport.resolve(A);
    // The transport callback has run, but the public request is still pending.
    await Promise.resolve();
    invalidateAuthSessionCache();
    expect(await result).toBe("AbortError");
});

test("superseded error is obsolete; current infrastructure failure is not anonymous", async () => {
    const old = deferred<AuthSessionPayload>();
    const failure = new Error("database unavailable");
    get = spyOn(http, "get").mockReturnValueOnce(old.promise as never).mockRejectedValueOnce(failure).mockResolvedValueOnce(B as never);
    const oldResult = getAuthSession().catch((error: Error) => error.name);
    invalidateAuthSessionCache();
    await expect(getAuthSession()).rejects.toBe(failure);
    old.reject(new Error("obsolete network error"));
    expect(await oldResult).toBe("AbortError");
    expect(await getAuthSession()).toEqual(B);
    expect(get).toHaveBeenCalledTimes(3);
});

const mutations = [
    ["login", () => login({ username: "B", password: "test-only" })],
    ["register", () => register({ username: "B", password: "test-only", acceptedTerms: true })],
    ["logout", () => logout()],
    ["reset", () => resetPassword({ email: "test@example.invalid", emailCode: "000000", password: "test-only" })],
] as const;

for (const [name, mutate] of mutations) {
    for (const succeeds of [true, false]) {
        test(`${name} invalidates before and after ${succeeds ? "success" : "failure"}, without fabricating auth`, async () => {
            const old = deferred<AuthSessionPayload>();
            const during = deferred<AuthSessionPayload>();
            const mutation = deferred<unknown>();
            get = spyOn(http, "get").mockReturnValueOnce(old.promise as never).mockReturnValueOnce(during.promise as never).mockResolvedValueOnce(B as never);
            post = spyOn(http, "post").mockReturnValueOnce(mutation.promise as never);
            const oldResult = getAuthSession().catch((error: Error) => error.name);
            const failure = new Error("mutation failed");
            const result = mutate().catch((error: unknown) => error);
            const duringResult = getAuthSession().catch((error: Error) => error.name);
            old.resolve(A);
            expect(await oldResult).toBe("AbortError");
            if (succeeds) mutation.resolve({ ok: true });
            else mutation.reject(failure);
            expect(await result).toEqual(succeeds ? { ok: true } : failure);
            during.resolve(A);
            expect(await duringResult).toBe("AbortError");
            expect(await getAuthSession()).toEqual(B);
            expect(get).toHaveBeenCalledTimes(3);
        });
    }
}
