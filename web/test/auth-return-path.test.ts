import { expect, test } from "bun:test";
import type { NavigateFunction } from "react-router";
import { returnAfterAuthentication, safeAuthReturnPath } from "../src/lib/auth-return-path";

test("authentication return refuses external, ambiguous and recursive paths", () => {
    for (const value of [null, "https://evil.invalid", "//evil.invalid", "/\\evil.invalid", "/%2f%2fevil.invalid", "/%255cevil", "/x\n//evil", "/%0aevil", "/login", "/x/../register"]) {
        expect(safeAuthReturnPath(value)).toBe("/");
    }
    expect(safeAuthReturnPath("/agri/deep?workspace_id=abc#map")).toBe("/");
    expect(safeAuthReturnPath("/agri/workspaces/abc")).toBe("/agri/workspaces/abc");
    for (const path of ['/agri/workspaces/abc?workspace_id=other', '/agri/?tenant_id=admin', '/agri/workspaces/abc#map', '/agri/unused/../workspaces/abc', '/other/../agri/workspaces/abc']) expect(safeAuthReturnPath(path)).toBe('/');
    expect(safeAuthReturnPath("/workspaces?filter=%2Fown")).toBe("/workspaces?filter=%2Fown");
});

test("same-origin Vue handoff loads a document; legacy React routes stay client-side", () => {
    const client: unknown[][] = [];
    const documents: string[] = [];
    const navigate = ((...args: unknown[]) => { client.push(args); }) as NavigateFunction;
    const location = { replace: (target: string) => { documents.push(target); } };
    returnAfterAuthentication("/agri/workspaces/a", navigate, location);
    returnAfterAuthentication("/agri", navigate, location);
    returnAfterAuthentication("/agriculture", navigate, location);
    returnAfterAuthentication("//evil.invalid", navigate, location);
    expect(documents).toEqual(["/agri/workspaces/a", "/agri/"]);
    expect(client).toEqual([["/agriculture", { replace: true }], ["/", { replace: true }]]);
});
