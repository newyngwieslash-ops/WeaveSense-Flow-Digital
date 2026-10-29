import { afterEach, expect, test } from "bun:test";
import { EventEmitter } from "node:events";
import type { Live2DLoader, Live2DLoaderContext } from "pixi-live2d-display/cubism4";
import { installLive2DLoader } from "../src/services/live2d-loader";

const originalFetch = globalThis.fetch;
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
afterEach(() => {
    globalThis.fetch = originalFetch;
    if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
    else Reflect.deleteProperty(globalThis, "window");
});

for (const outcome of ["success", "http-error", "network-error"] as const) {
    test(`destroy listener retains receiver and detaches original target after ${outcome}`, async () => {
        Object.defineProperty(globalThis, "window", { configurable: true, value: { location: { href: "https://canvas.example/" } } });
        const loader = { middlewares: [] } as unknown as typeof Live2DLoader;
        installLive2DLoader(loader);
        const original = new EventEmitter();
        const replacement = new EventEmitter();
        const unrelated = () => {};
        replacement.on("destroy", unrelated);
        const context: Live2DLoaderContext = {
            url: "/api/public/appearance/live2d/id/model.model3.json",
            type: "json",
            target: original as unknown as Live2DLoaderContext["target"],
        };
        globalThis.fetch = (async () => {
            expect(original.listenerCount("destroy")).toBe(1);
            // The shared context can change during an awaited resource request.
            context.target = replacement as unknown as Live2DLoaderContext["target"];
            if (outcome === "network-error") throw new Error("network unavailable");
            return new Response('{"Version":3}', { status: outcome === "success" ? 200 : 403 });
        }) as typeof fetch;
        const pending = loader.middlewares[0](context, async () => {});
        if (outcome === "success") {
            await pending;
            expect(context.result).toEqual({ Version: 3 });
        } else {
            await expect(pending).rejects.toThrow(outcome === "http-error" ? "403" : "network unavailable");
        }
        expect(original.listenerCount("destroy")).toBe(0);
        expect(replacement.listeners("destroy")).toEqual([unrelated]);
    });
}
