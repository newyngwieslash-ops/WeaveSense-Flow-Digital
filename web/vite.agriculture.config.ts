import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { agricultureBuildPolicy } from "./scripts/agriculture-build-policy";

const root = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
    root,
    publicDir: false,
    plugins: [react(), agricultureBuildPolicy(root), {
        name: "agriculture-product-entry",
        transformIndexHtml: { order: "pre", handler(html) {
            return html.replace('src="/src/main.tsx"', 'src="/src/features/agriculture/main.tsx"')
                .replace("AI 影视与短剧创作工作台", "WeaveSense-DigitalTwin 农业空间工作台")
                .replace("<title>正在加载</title>", "<title>WeaveSense-DigitalTwin</title>");
        } },
    }],
    resolve: { alias: [
        { find: "@/lib/user-session", replacement: resolve(root, "src/features/agriculture/session.ts") },
        { find: "@", replacement: resolve(root, "src") },
    ] },
    define: { __APP_VERSION__: JSON.stringify("agriculture-v1"), __APP_CHANGELOG__: JSON.stringify("") },
    server: { host: "127.0.0.1", proxy: { "/api": { target: process.env.VITE_API_PROXY_TARGET || "http://127.0.0.1:8080", changeOrigin: true } } },
    build: { outDir: "dist-agriculture", emptyOutDir: true },
});
