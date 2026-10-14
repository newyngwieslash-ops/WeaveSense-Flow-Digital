import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

function source(path: string) {
    return readFileSync(resolve(import.meta.dir, path), "utf8");
}

describe("workspace route loading", () => {
    test("keeps lazy workspace routes inside the workspace stage", () => {
        const router = source("../src/router.tsx");
        const deferred = router.slice(router.indexOf("function deferred"), router.indexOf("function fullScreenDeferred"));

        expect(deferred).toContainCode("<WorkspaceRouteLoader />");
        expect(deferred).not.toContainCode("FullScreenLoader");
        expect(router).toContainCode("fullScreenDeferred(<LoginPage />)");
        expect(router).toContainCode("fullScreenDeferred(<SharedCanvasPage />)");
    });

    test("preloads the reported workspace routes before navigation", () => {
        const modules = source("../src/lib/workspace-route-modules.ts");
        const navigation = source("../src/components/layout/workspace-sidebar-nav.tsx");

        for (const route of ["projects", "canvas", "assets", "create"]) {
            expect(modules).toContainCode(`${route}: () => import`);
        }
        expect(modules).toContainCode('projectDetail: () => import("@/pages/projects/detail")');
        expect(modules).toContainCode('slug === "projects" && segments.length > 1');
        expect(navigation).toContainCode("onPointerEnter={() => preloadWorkspaceRoute(linkTo)}");
        expect(navigation).toContainCode("onPointerDown={() => preloadWorkspaceRoute(linkTo)}");
        expect(navigation).toContainCode("onFocus={() => preloadWorkspaceRoute(linkTo)}");
    });

    test("keeps the creation page at root and preserves the create compatibility route", () => {
        const router = source("../src/router.tsx");
        const navigation = source("../src/components/layout/workspace-sidebar-nav.tsx");

        expect(router).toContainCode('{ path: "/", element: deferred(<CreatePage />) }');
        expect(router).toContainCode('{ path: "/create", element: deferred(<CreatePage />) }');
        expect(router).toContainCode("<RequireAuth allowAnonymous={isGuestWorkspacePath(pathname)}>");
        expect(router).toContainCode('{ path: "/assets", element: <RequireAuth>{deferred(<AssetsPage />)}</RequireAuth> }');
        expect(router).toContainCode('{ path: "/canvas/:id", element: <RequireAuth><CanvasProjectPage /></RequireAuth> }');
        expect(router).not.toContainCode('path: "/home"');
        expect(router).not.toContainCode("HomePage");
        expect(navigation).toContainCode('{ ...toolItem("create", "/"), id: "home", title: "创作" }');
        expect(navigation).not.toContainCode('to: "/create"');
        expect(navigation).not.toContainCode('to: "/home"');
    });

    test("preloads canvas detail and paints opening feedback before navigation", () => {
        const modules = source("../src/lib/workspace-route-modules.ts");
        const router = source("../src/router.tsx");
        const canvasLibrary = source("../src/pages/canvas/index.tsx");
        const canvasCard = source("../src/components/canvas/canvas-folder-card.tsx");

        expect(modules).toContainCode('loadCanvasProjectPage = () => import("@/pages/canvas/project")');
        expect(router).toContainCode("lazy(loadCanvasProjectPage)");
        expect(canvasLibrary).toContainCode("setOpeningProjectId(id)");
        expect(canvasLibrary).toContainCode("window.requestAnimationFrame(() => navigate(");
        expect(canvasCard).toContainCode("onPointerEnter={onPrefetch}");
        expect(canvasCard).toContainCode("正在打开");
    });

    test("loads only the active project detail view and keeps canvas-only state out of the global layout", () => {
        const detail = source("../src/pages/projects/detail.tsx");
        const layout = source("../src/layouts/user-layout.tsx");
        const canvas = source("../src/pages/canvas/index.tsx");

        for (const view of ["assets", "canvases", "chapters", "overview", "settings", "workflow", "editor"]) {
            expect(detail).toContainCode(`lazy(() => import("./detail/${view}"))`);
        }
        expect(layout).not.toContainCode("useCanvasUiStore");
        expect(layout).not.toContainCode("CanvasDeleteProjectsDialog");
        expect(canvas).toContainCode("deleteDialogOpen ? <Suspense");
        expect(detail).toContainCode("project-workspace-header");
        expect(detail).not.toContainCode("useWorkspaceTopBarExtension");
        expect(detail).toContainCode("新建画布");
    });

    test("keeps project asset refresh scoped to the latest user and project", () => {
        const editor = source("../src/pages/projects/detail/editor.tsx");

        expect(editor).toContainCode("const assetOwnerKey = JSON.stringify([scope, projectId])");
        expect(editor).toContainCode("activeAssetOwnerKeyRef.current !== requestedOwnerKey");
        expect(editor).toContainCode("assetRefreshSequenceRef.current !== requestSequence");
        expect(editor).toContainCode("assetRefreshSequenceRef.current += 1");
        expect(editor).toContainCode("syncedAssetOwnerKeyRef.current = assetOwnerKey");
        expect(editor.match(/listProjectAssets\(projectId\)/g)).toHaveLength(1);
    });

    test("does not poll wallet balance from permanent workspace chrome", () => {
        const wallet = source("../src/hooks/use-wallet-balance.ts");
        expect(wallet).not.toContainCode("refetchInterval:");
        expect(wallet).toContainCode("wallet:updated");
        expect(wallet).toContainCode("WALLET_STALE_TIME_MS");
    });

    test("defers modal-only markdown and canvas creation runtimes until interaction", () => {
        const changelogButton = source("../src/components/layout/app-changelog-modal.tsx");
        const announcements = source("../src/components/layout/system-announcement-center.tsx");
        const projectDetail = source("../src/pages/projects/detail.tsx");
        const workflow = source("../src/pages/projects/detail/workflow-production-workbench.tsx");

        expect(changelogButton).toContainCode('lazy(() => import("@/components/layout/app-changelog-dialog")');
        expect(changelogButton).not.toContainCode('from "react-markdown"');
        expect(announcements).toContainCode('lazy(() => import("@/components/ui/aceternity/announcement-timeline-modal")');
        expect(projectDetail).toContainCode('import("@/services/user-data-sync")');
        expect(projectDetail).not.toContainCode('import { createCanvasProjectWithRemoteSync } from "@/services/user-data-sync"');
        expect(workflow).not.toContainCode('from "@/lib/video-poster"');
        expect(workflow).toContainCode("if (playing) return <video");
    });

    test("uses a quiet workspace skeleton for initial hydration", () => {
        const loader = source("../src/components/ui/aceternity/full-screen-loader.tsx");
        const css = source("../src/styles/globals.css");

        expect(loader).toContainCode("full-screen-loader-scene");
        expect(loader).toContainCode("full-screen-loader-guide");
        expect(loader).toContainCode("LoadingSignal");
        expect(loader).not.toContainCode("YINGCE STUDIO");
        expect(loader).not.toContainCode("loading-cue");
        expect(css).toContainCode("@keyframes loading-signal-spin");
        expect(css).toContainCode("@media (prefers-reduced-motion: reduce)");
        expect(css).not.toContainCode("@keyframes loading-cue-pulse");
    });
});

describe("workspace wallet entry", () => {
    test("opens the credits modal instead of a dedicated wallet page", () => {
        const router = source("../src/router.tsx");
        const modules = source("../src/lib/workspace-route-modules.ts");
        const host = source("../src/components/layout/workspace-wallet-modal.tsx");
        const palette = source("../src/components/layout/workspace-command-palette.tsx");
        const canvasTopBar = source("../src/pages/canvas/canvas-project-top-bar.tsx");
        const topBar = source("../src/components/layout/workspace-top-bar.tsx");
        const css = source("../src/styles/globals.css");

        expect(router).toContainCode('path: "/wallet"');
        expect(router).toContainCode("element: <RequireAuth>{null}</RequireAuth>");
        expect(router).not.toContainCode("WalletPage");
        expect(router).not.toContainCode("loadWalletPage");
        expect(modules).not.toContainCode("pages/wallet");
        expect(host).toContainCode('pathname !== "/wallet"');
        expect(host).toContainCode("openWorkspaceWallet");
        expect(palette).toContainCode("run: () => openWorkspaceWallet()");
        expect(palette).not.toContainCode('"/wallet"');
        expect(canvasTopBar).toContainCode("openWorkspaceWallet()");
        expect(canvasTopBar).not.toContainCode('to="/wallet"');
        expect(topBar).toContainCode("openWorkspaceWallet()");
        expect(css).not.toContainCode(".wallet-library-page");
        expect(css).not.toContainCode(".wallet-market-page");
    });
});
