// P4 connectors for flow sources. Reuses WeaveSense's Telethon worker contract
// (POST /v1/history, incremental by min_id), the t.me/s web-preview parser and
// the WeChat auth-gateway Atom feed (with its backup-provider metadata).
// Media (loom collection-reliability worker): /v1/history returns telegram://<channel>/<id>
// attachment refs; each is then stored through the worker's authenticated /v1/media
// (streamed, size-checked, sha256) within a per-batch budget → status "stored" with the
// object key. Older workers that report media_status inline are still understood.
// t.me/s preview links are not downloaded ("unsupported").
import { parseTelegramHtml } from "../src/lib/server/telegram.ts";
import { parseFeedXml } from "../src/lib/server/rss-parser.ts";
import type { FeedItem } from "./engine.ts";

export type ConnectorEnv = {
  telegramWorkerUrl?: string;
  wechatGatewayUrl?: string;
  wechatGatewayToken?: string;
  /** Bearer for the Telethon worker's /v1/media (TELEGRAM_MEDIA_TOKEN). */
  telegramMediaToken?: string;
  fetchImpl?: typeof fetch;
  /** Fetches public HTML (t.me/s preview). Defaults to WeaveSense's outbound-policy guarded fetch. */
  fetchPublicText?: (url: string) => Promise<string>;
  /** "fixture" when the transport is a test double; recorded on acceptance samples. */
  mode?: "live" | "fixture";
};
export type MediaRef = { url: string; type: string; status: "stored" | "unsupported" | "too_large" | "timeout" | "failed" | "disabled"; reason: string;
  /** Original telegram://channel/id locator (kept so a failed download can be retried). */ ref?: string; attempts?: number; sha256?: string; content_type?: string };
export type MediaScope = { tenant_id: string; workspace_id: string };
/** §10.4: object keys carry tenant and workspace. Legacy (pre-scope) keys are telegram/<channel>/<id>. */
export const SCOPED_MEDIA_KEY = /^tenants\/([A-Za-z0-9_-]{1,64})\/workspaces\/([A-Za-z0-9_-]{1,64})\/telegram\/[a-z0-9_]+\/\d+$/;
export const LEGACY_MEDIA_KEY = /^telegram\/([a-z0-9_]+)\/(\d+)$/;
export const scopedMediaKey = (sc: MediaScope, legacyKey: string) => `tenants/${sc.tenant_id}/workspaces/${sc.workspace_id}/${legacyKey}`;
const SCOPE_ID = /^[A-Za-z0-9_-]{1,64}$/;
export type ConnectorItem = FeedItem & { media?: MediaRef[] };
export type FeedValidators = { etag: string; last_modified: string; identity: string };
export type ConnectorBatch = { items: ConnectorItem[]; collector: string; provider?: string; mode: "live" | "fixture";
  /** RSS only: conditional-request state to persist with the cursor, 304 flag, entries rejected by the parser. */
  feed_validators?: FeedValidators; not_modified?: boolean; rejected_entries?: number };
export type SourceRow = { id: string; kind: string; url: string; config: any; tenant_id?: string; workspace_id?: string; last_external_id?: string | null; feed_validators?: FeedValidators | null };

const MEDIA_REASON = "preview link only; media is downloaded by the Telethon worker";
const MEDIA_SKIP: Record<string, string> = { too_large: "exceeds TELEGRAM_MEDIA_MAX_BYTES", timeout: "download timed out", failed: "download failed", disabled: "object storage not configured" };
export const telegramMessageId = (externalId: string) => Number(String(externalId).match(/(\d+)$/)?.[1] ?? NaN);
export const normalizeHandle = (h: string) => String(h ?? "").trim().replace(/^https?:\/\/t\.me\/(s\/)?/i, "").replace(/^@/, "").split(/[/?#]/)[0];

const MEDIA_REF = /^telegram:\/\/([a-z0-9_]{5,32})\/([1-9]\d{0,30})$/;
function toItem(it: any, kind: string): ConnectorItem {
  const type = String(it.media_type ?? "media");
  const urls: string[] = (Array.isArray(it.media_urls) ? it.media_urls : []).filter(Boolean);
  let media: MediaRef[];
  if (urls.length && urls.every((u) => MEDIA_REF.test(u))) media = urls.map((url) => ({ url, ref: url, type, status: "failed", reason: "pending", attempts: 0 }));
  else if (it.media_status === "saved") media = urls.map((url) => ({ url, type, status: "stored", reason: "" }));
  else if (MEDIA_SKIP[it.media_status]) media = [{ url: "", type, status: it.media_status, reason: MEDIA_SKIP[it.media_status] }];
  else media = urls.map((url) => ({ url, type, status: "unsupported", reason: MEDIA_REASON }));
  const body = String(it.body ?? "");
  return { external_id: String(it.external_id), title: String(it.title ?? "").slice(0, 300) || (kind === "telegram" ? body.slice(0, 80) : ""),
    body, url: String(it.url ?? ""), published_at: it.published_at ?? null, media };
}

function noteMissing(item: ConnectorItem): ConnectorItem {
  const missing = (item.media ?? []).filter((m) => m.status !== "stored");
  return missing.length ? { ...item, body: `${item.body}\n\n[${missing.length} media attachment(s) not collected: ${missing[0].status}]` } : item;
}

const MEDIA_PER_BATCH = 10;
const MEDIA_BATCH_BUDGET_MS = 90_000;
const MEDIA_HTTP: Record<number, [MediaRef["status"], string]> = {
  413: ["too_large", "exceeds the worker media limit"], 504: ["timeout", "download timed out"],
  503: ["disabled", "worker media storage unavailable"], 404: ["failed", "media not found"],
};

/** Stores pending telegram:// attachments through the worker's /v1/media, bounded per batch. */
export async function storeTelegramMedia(env: ConnectorEnv, items: ConnectorItem[], now: () => number = Date.now, scope?: MediaScope): Promise<void> {
  const pending = items.flatMap((it) => (it.media ?? []).filter((m) => m.reason === "pending"));
  if (!pending.length) return;
  const f = env.fetchImpl ?? fetch;
  const start = now();
  let stop = "";
  for (const [i, m] of pending.entries()) {
    const ref = MEDIA_REF.exec(m.ref ?? m.url)!;
    m.attempts = (m.attempts ?? 0) + 1;
    if (!scope || !SCOPE_ID.test(scope.tenant_id) || !SCOPE_ID.test(scope.workspace_id)) { Object.assign(m, { status: "failed", reason: "media scope (tenant/workspace) missing" }); continue; }
    if (!env.telegramMediaToken || !env.telegramWorkerUrl) { Object.assign(m, { status: "disabled", reason: "TELEGRAM_MEDIA_TOKEN not configured" }); continue; }
    if (stop) { Object.assign(m, { status: "failed", reason: stop }); continue; }
    if (i >= MEDIA_PER_BATCH || now() - start > MEDIA_BATCH_BUDGET_MS) { Object.assign(m, { status: "failed", reason: "media batch budget exhausted" }); continue; }
    try {
      const res = await f(`${env.telegramWorkerUrl.replace(/\/$/, "")}/v1/media`, { method: "POST", redirect: "error", signal: AbortSignal.timeout(130_000),
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${env.telegramMediaToken}` },
        body: JSON.stringify({ channel: ref[1], message_id: ref[2], tenant_id: scope.tenant_id, workspace_id: scope.workspace_id }) });
      const body: any = await res.json().catch(() => ({}));
      const key = typeof body.object_key === "string" ? body.object_key : "";
      const k = SCOPED_MEDIA_KEY.exec(key);
      if (res.ok && k && k[1] === scope.tenant_id && k[2] === scope.workspace_id) {
        Object.assign(m, { url: key, status: "stored", reason: "", sha256: typeof body.sha256 === "string" ? body.sha256 : undefined, content_type: typeof body.content_type === "string" ? body.content_type : undefined });
      } else if (res.ok && LEGACY_MEDIA_KEY.test(key)) {
        // A worker without scope support stored it under the shared prefix: not accepted (§10.4); retried after upgrade.
        Object.assign(m, { status: "failed", reason: "worker returned an unscoped object key; upgrade the Telethon worker" });
      } else if (res.status === 429) {
        stop = "telegram flood wait; later attachments skipped this run";
        Object.assign(m, { status: "failed", reason: stop });
      } else {
        const [status, reason] = MEDIA_HTTP[res.status] ?? ["failed", `worker media HTTP ${res.status}`];
        Object.assign(m, { status, reason });
      }
    } catch (e: any) {
      Object.assign(m, e?.name === "TimeoutError" ? { status: "timeout", reason: "download timed out" } : { status: "failed", reason: "worker unreachable" });
    }
  }
}

/**
 * Telegram: Telethon incremental history when a worker is configured, else the
 * public web preview. Only messages with id > cursor are returned, so a preview
 * page that repeats old posts can never move the cursor backwards or re-ingest.
 */
export async function fetchTelegram(env: ConnectorEnv, src: SourceRow): Promise<ConnectorBatch> {
  const handle = normalizeHandle(src.config?.handle ?? src.url);
  if (!/^[A-Za-z0-9_]{4,64}$/.test(handle)) throw new Error("telegram: invalid channel handle");
  const minId = Number.isFinite(telegramMessageId(src.last_external_id ?? "")) ? telegramMessageId(src.last_external_id!) : 0;
  const mode = env.mode ?? "live";
  let raw: any[]; let collector: string;
  if (env.telegramWorkerUrl) {
    const f = env.fetchImpl ?? fetch;
    const res = await f(`${env.telegramWorkerUrl.replace(/\/$/, "")}/v1/history`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ channel: handle, min_id: String(minId), limit: 80 }), signal: AbortSignal.timeout(60_000) });
    const body: any = await res.json().catch(() => ({}));
    if (body.flood_wait) throw new Error(`telegram: FloodWait ${body.flood_wait}s`);
    if (!res.ok || body.error) throw new Error(`telegram: ${body.error || `worker HTTP ${res.status}`}`);
    raw = body.items ?? []; collector = "telethon";
  } else {
    const get = env.fetchPublicText ?? (async (u: string) => (await import("../src/lib/server/http.ts")).fetchText(u, 12000));
    raw = parseTelegramHtml(await get(`https://t.me/s/${encodeURIComponent(handle)}`), handle); collector = "t.me/s";
  }
  const fresh = raw.map((x) => toItem(x, "telegram"))
    .filter((x) => Number.isFinite(telegramMessageId(x.external_id)) && telegramMessageId(x.external_id) > minId)
    .sort((a, b) => telegramMessageId(a.external_id) - telegramMessageId(b.external_id));
  await storeTelegramMedia(env, fresh, Date.now, src.tenant_id && src.workspace_id ? { tenant_id: src.tenant_id, workspace_id: src.workspace_id } : undefined);
  return { items: fresh.map(noteMissing), collector, mode };
}

const WECHAT_PROVIDERS = ["", "primary", "dajiala", "mixed"];
const WECHAT_MAX_CHARS = 8 * 1024 * 1024;

/** WeChat OA: requires the operator's auth gateway and a per-source gateway feed id (external feed config). */
export async function fetchWechat(env: ConnectorEnv, src: SourceRow): Promise<ConnectorBatch> {
  const feedId = String(src.config?.gateway_source_id ?? "");
  if (!env.wechatGatewayUrl || !env.wechatGatewayToken) throw new Error("wechat: gateway not configured (WECHAT_GATEWAY_URL / WECHAT_GATEWAY_TOKEN)");
  if (!/^[A-Za-z0-9._:-]{1,128}$/.test(feedId)) throw new Error("wechat: source config.gateway_source_id required");
  const url = new URL(env.wechatGatewayUrl);
  url.pathname = `${url.pathname.replace(/\/+$/, "")}/v1/feeds/${encodeURIComponent(feedId)}.atom`;
  const f = env.fetchImpl ?? fetch;
  // Internal gateway: bearer token, redirects refused so the token can't leak.
  const res = await f(url.toString(), { headers: { Accept: "application/atom+xml", Authorization: `Bearer ${env.wechatGatewayToken}` }, redirect: "error", signal: AbortSignal.timeout(30_000) });
  if (!res.ok) throw new Error(`wechat: gateway HTTP ${res.status}`);
  const provider = res.headers.get("x-loom-provider") ?? "";
  if (!WECHAT_PROVIDERS.includes(provider)) throw new Error("wechat: invalid gateway metadata");
  const text = await res.text();
  if (text.length > WECHAT_MAX_CHARS) throw new Error("wechat: feed too large");
  const items = parseFeedXml(text, src.id, "wechat" as any).map((x) => noteMissing(toItem(x, "wechat")));
  return { items, collector: "wechat_gateway", provider: provider || "primary", mode: env.mode ?? "live" };
}

export function connectorEnvFromProcess(): ConnectorEnv {
  const e = (k: string) => process.env[k]?.trim() || undefined;
  return { telegramWorkerUrl: e("TELEGRAM_WORKER_URL"), wechatGatewayUrl: e("WECHAT_GATEWAY_URL"), wechatGatewayToken: e("WECHAT_GATEWAY_TOKEN"), telegramMediaToken: e("TELEGRAM_MEDIA_TOKEN") };
}

/** Minimal main-text extraction: drops boilerplate tags and picks <article>/<main> or the densest paragraph block. */
export function extractMainText(html: string): { title: string; text: string } {
  const title = (html.match(/<title[^>]*>([\s\S]*?)<\/title>/i)?.[1] ?? "").replace(/\s+/g, " ").trim();
  let h = html.replace(/<(script|style|noscript|nav|header|footer|aside|form|svg)[\s\S]*?<\/\1>/gi, " ");
  const main = h.match(/<(article|main)[^>]*>([\s\S]*?)<\/\1>/i)?.[2];
  if (main) h = main;
  const paras = [...h.matchAll(/<p[^>]*>([\s\S]*?)<\/p>/gi)].map((m) => m[1]);
  const strip = (s: string) => s.replace(/<br\s*\/?>/gi, "\n").replace(/<[^>]+>/g, " ")
    .replace(/&nbsp;/g, " ").replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#39;/g, "'")
    .replace(/[ \t]+/g, " ").replace(/\n\s*\n+/g, "\n").trim();
  const body = paras.map(strip).filter((p) => p.length > 20).join("\n\n");
  const text = (body || strip(h)).slice(0, 200_000);
  return { title, text };
}
