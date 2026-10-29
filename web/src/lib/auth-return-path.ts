import type { NavigateFunction } from "react-router";

const origin = "https://auth-return.invalid";

export function safeAuthReturnPath(value: string | null): string {
    if (!value || !value.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u0020\u007f]/.test(value)) return "/";
    const path = value.split(/[?#]/, 1)[0];
    // Reject encoded path separators rather than relying on different proxy/router
    // decode rules. Query values remain untouched and cannot change the origin.
    if (/%(?:2f|5c|25|0[0-9a-f]|1[0-9a-f]|7f)/i.test(path)) return "/";
    try {
        const target = new URL(value, origin);
        if (target.origin !== origin || ["/login", "/register", "/forgot-password"].includes(target.pathname)) return "/";
        return target.pathname + target.search + target.hash;
    } catch {
        return "/";
    }
}

export function returnAfterAuthentication(
    value: string,
    navigate: NavigateFunction,
    location: Pick<Location, "replace"> = window.location,
) {
    const target = safeAuthReturnPath(value);
    const path = target.split(/[?#]/, 1)[0];
    // /agri belongs to the same-origin Vue application, not the React router.
    if (path === "/agri" || path.startsWith("/agri/")) location.replace(target);
    else navigate(target, { replace: true });
}
