import type { AxiosInstance } from 'axios';
import { publishSessionHint } from '../../../../contracts/agriculture/session-hints';
import { isRoutineChecking } from './routine-session';

// Installed only by the agriculture entry. Film's existing HTTP policy is unchanged.
export function installAgricultureRequestSecurity(client: AxiosInstance, fetcher: typeof fetch = fetch) {
    const writing = (method?: string) => !['GET', 'HEAD', 'OPTIONS'].includes((method || 'GET').toUpperCase());
    const requestId = client.interceptors.request.use(async config => {
        if (config.baseURL !== '/api' || !config.url?.startsWith('/') || config.url.startsWith('//')) throw new Error('农业请求必须使用同源平台接口。');
        if (!writing(config.method)) return config;
        if (isRoutineChecking()) throw new Error('正在重新验证当前权限，请稍后操作。');
        const response = await fetcher('/api/auth/csrf', { credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: config.signal as AbortSignal, headers: { Accept: 'application/json' } });
        if (!response.ok) throw new Error('请求安全验证失败，请重新验证会话。');
        let envelope: unknown;
        try { envelope = await response.json(); } catch { throw new Error('请求安全验证格式异常。'); }
        const value = envelope as { code?: unknown; data?: { token?: unknown } } | null;
        if (!value || value.code !== 0 || typeof value.data?.token !== 'string' || !/^[a-f0-9]{64}$/.test(value.data.token)) throw new Error('请求安全验证格式异常。');
        config.headers.set('X-CSRF-Token', value.data.token);
        return config;
    });
    const complete = (config?: { method?: string; url?: string; headers?: { delete(name: string): unknown } }) => {
        config?.headers?.delete('X-CSRF-Token');
        if (writing(config?.method) && /^\/(?:agriculture\/|auth\/(?:login|register|logout|password-reset)$)/.test(config?.url || '')) publishSessionHint();
    };
    const responseId = client.interceptors.response.use(response => { complete(response.config); return response; }, error => {
        complete(error?.config);
        throw error;
    });
    return () => { client.interceptors.request.eject(requestId); client.interceptors.response.eject(responseId); };
}
