import React, { useEffect, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { App, Alert, Button, ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider, Link, Navigate, Outlet, useSearchParams } from "react-router";
import "@fontsource-variable/inter";
import "antd/dist/reset.css";
import "@/styles/globals.css";
import "@/styles/auth-input-fix.css";
import "./profile.css";
import LoginPage from "@/pages/auth/login";
import RegisterPage from "@/pages/auth/register";
import ForgotPasswordPage from "@/pages/auth/forgot-password";
import { RequireAuth } from "@/components/auth/require-auth";
import { getAuthSession, invalidateAuthSessionCache, logout } from "@/services/api/auth";
import { agricultureAPI } from "@/services/api/agriculture";
import { agricultureWorkspaceURL } from '../../../../contracts/agriculture/navigation';
import { safeAuthReturnPath } from '@/lib/auth-return-path';
import { useUserStore } from "@/stores/use-user-store";
import { appQueryClient } from "@/lib/query-client";
import { getAntThemeConfig } from "@/lib/app-theme";
import { applyUserSession, beginSessionRevalidation } from "./session";
import OrganizationPage from "./organization";
import { apiClient } from '@/services/api/request';
import { installAgricultureRequestSecurity } from './request-security';
import { SESSION_RECHECK_MS, subscribeSessionHints } from '../../../../contracts/agriculture/session-hints';

installAgricultureRequestSecurity(apiClient);

function SessionBoundary({ children }: { children: ReactNode }) {
    const [error, setError] = useState(false);
    const [attempt, setAttempt] = useState(0);
    const hydrated = useUserStore((state) => state.hydrated);
    useEffect(() => {
        const revalidate = () => {
            invalidateAuthSessionCache();
            beginSessionRevalidation();
            setError(false);
            setAttempt((value) => value + 1);
        };
        const visible = () => { revalidate(); };
        const unsubscribe = subscribeSessionHints(revalidate);
        const timer = window.setInterval(() => { if (document.visibilityState === 'visible') revalidate(); }, SESSION_RECHECK_MS);
        window.addEventListener("focus", revalidate);
        window.addEventListener("pageshow", revalidate);
        window.addEventListener("popstate", revalidate);
        document.addEventListener("visibilitychange", visible);
        return () => {
            unsubscribe();
            window.clearInterval(timer);
            window.removeEventListener("focus", revalidate);
            window.removeEventListener("pageshow", revalidate);
            window.removeEventListener("popstate", revalidate);
            document.removeEventListener("visibilitychange", visible);
        };
    }, []);
    useEffect(() => {
        let active = true;
        beginSessionRevalidation();
        invalidateAuthSessionCache();
        if (document.visibilityState === 'hidden') return;
        getAuthSession().then((session) => { if (active) void applyUserSession(session); }).catch((error: unknown) => {
            if (!active) return;
            // A concurrent login/logout invalidates old requests. Recheck the new
            // server session instead of treating an obsolete result as identity.
            if (error instanceof Error && error.name === "AbortError") setAttempt((value) => value + 1);
            else setError(true);
        });
        return () => { active = false; };
    }, [attempt]);
    if (error) return <main className="agri-profile"><Alert type="error" title="平台会话不可用" description="尚未验证登录状态；不会载入任何工作区缓存。" /><Button onClick={() => { setError(false); setAttempt((n) => n + 1); }}>重试</Button></main>;
    if (!hydrated) return <main className="agri-profile" role="status">正在验证平台会话…</main>;
    return children;
}

function AuthLayout() {
    const [params] = useSearchParams();
    const next = encodeURIComponent(safeAuthReturnPath(params.get('next')));
    return <main className="agri-profile agri-auth"><h1>WeaveSense-DigitalTwin</h1><p>使用统一平台账号登录农业工作台。</p><nav aria-label="账号操作"><Link to={`/login?next=${next}`}>登录</Link><Link to={`/register?next=${next}`}>注册</Link></nav><Outlet /></main>;
}

function Home() {
    const { message } = App.useApp();
    const user = useUserStore((state) => state.user);
    const workspaces = useQuery({ queryKey: ["agriculture", "workspaces", user?.id], queryFn: async ({ signal }) => (await agricultureAPI.workspaces(signal)).filter(workspace => workspace.tenantStatus === "active"), enabled: Boolean(user) });
    const leave = async () => {
        try { await logout(); await applyUserSession({ user: null }); }
        catch { message.error("退出失败，请重试。当前登录状态未被伪装为已退出。"); }
    };
    return <main className="agri-profile">
        <header><h1>农业空间工作台</h1><Button onClick={leave}>退出登录</Button></header>
        <p data-testid="session-user" data-user-id={user?.id}>当前平台用户：{user?.displayName || user?.username}</p>
        <nav aria-label="农业组织导航"><Link to="/organization">管理组织与成员</Link></nav>
        <Alert type="info" title="农业构建基线" description="GIS 工作台集成与业务验收仍在实施。此页展示平台返回的真实工作区，不提供模拟农场或遥感结果。" />
        <h2>可访问工作区</h2>
        {workspaces.isPending ? <p role="status">正在读取工作区…</p> : workspaces.isError ? <Alert type="error" title="工作区读取失败" action={<Button onClick={() => void workspaces.refetch()}>重试</Button>} /> : <ul>{workspaces.data.length ? workspaces.data.map((workspace) => <li key={workspace.id}><a href={agricultureWorkspaceURL(workspace.id)}>{workspace.name}</a><span> · {workspace.slug}</span></li>) : <li>尚无可访问工作区。</li>}</ul>}
        <p>农业子应用使用同域 /agri；工作区选择只用于请求，资源权限仍由服务端验证。</p>
    </main>;
}

const router = createBrowserRouter([
    { element: <AuthLayout />, children: [{ path: "/login", element: <LoginPage /> }, { path: "/register", element: <RegisterPage /> }, { path: "/forgot-password", element: <ForgotPasswordPage /> }] },
    { path: "/", element: <RequireAuth><Home /></RequireAuth> },
    { path: "/organization", element: <RequireAuth><OrganizationPage /></RequireAuth> },
    { path: "*", element: <Navigate to="/" replace /> },
]);

createRoot(document.getElementById("root")!).render(<React.StrictMode><ConfigProvider locale={zhCN} theme={getAntThemeConfig(false)}><App><QueryClientProvider client={appQueryClient}><SessionBoundary><RouterProvider router={router} /></SessionBoundary></QueryClientProvider></App></ConfigProvider></React.StrictMode>);
