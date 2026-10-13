// P4 connectors for flow sources. Reuses WeaveSense's Telethon worker contract
// (POST /v1/history, incremental by min_id), the t.me/s web-preview parser and
// the WeChat auth-gateway Atom feed (with its backup-provider metadata).
// Media: the Telethon worker downloads photo/video/audio/file media into object
// storage within size/time limits (media_status=saved → status "stored"); skipped
// media keeps the worker's reason. t.me/s preview links are not downloaded ("unsupported").
import { parseTelegramHtml } from "../src/lib/server/telegram.ts";
import { parseFeedXml } from "../src/lib/server/rss-parser.ts";
import type { FeedItem } from "./engine.ts";

export type ConnectorEnv = {
  telegramWorkerUrl?: string;
  wechatGatewayUrl?: string;
  wechatGatewayToken?: string;
  fetchImpl?: typeof fetch;
  /** Fetches public HTML (t.me/s preview). Defaults to WeaveSense's outbound-policy guarded fetch. */
  fetchPublicText?: (url: string) => Promise<string>;
  /** "fixture" when the transport is a test double; recorded on acceptance samples. */
  mode?: "live" | "fixture";
};
export type MediaRef = { url: string; type: string; status: "stored" | "unsupported" | "too_large" | "timeout" | "failed" | "disabled"; reason: string };
export type ConnectorItem = FeedItem & { media?: MediaRef[] };
export type ConnectorBatch = { items: ConnectorItem[]; collector: string; provider?: string; mode: "live" | "fixture" };
export type SourceRow = { id: string; kind: string; url: string; config: any; last_external_id?: string | null };

const MEDIA_REASON = "preview link only; media is downloaded by the Telethon worker";
const MEDIA_SKIP: Record<string, string> = { too_large: "exceeds TELEGRAM_MEDIA_MAX_BYTES", timeout: "download timed out", failed: "download failed", disabled: "object storage not configured" };
export const telegramMessageId = (externalId: string) => Number(String(externalId).match(/(\d+)$/)?.[1] ?? NaN);
export const normalizeHandle = (h: string) => String(h ?? "").trim().replace(/^https?:\/\/t\.me\/(s\/)?/i, "").replace(/^@/, "").split(/[/?#]/)[0];

function toItem(it: any, kind: string): ConnectorItem {
  const type = String(it.media_type ?? "media");
  const urls: string[] = (Array.isArray(it.media_urls) ? it.media_urls : []).filter(Boolean);
  let media: MediaRef[];
  if (it.media_status === "saved") media = urls.map((url) => ({ url, type, status: "stored", reason: "" }));
  else if (MEDIA_SKIP[it.media_status]) media = [{ url: "", type, status: it.media_status, reason: MEDIA_SKIP[it.media_status] }];
  else media = urls.map((url) => ({ url, type, status: "unsupported", reason: MEDIA_REASON }));
  const missing = media.filter((m) => m.status !== "stored");
  const body = String(it.body ?? "") + (missing.length ? `\n\n[${missing.length} media attachment(s) not collected: ${missing[0].status}]` : "");
  return { external_id: String(it.external_id), title: String(it.title ?? "").slice(0, 300) || (kind === "telegram" ? body.slice(0, 80) : ""),
    body, url: String(it.url ?? ""), published_at: it.published_at ?? null, media };
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
  const items = raw.map((x) => toItem(x, "telegram"))
    .filter((x) => Number.isFinite(telegramMessageId(x.external_id)) && telegramMessageId(x.external_id) > minId)
    .sort((a, b) => telegramMessageId(a.external_id) - telegramMessageId(b.external_id));
  return { items, collector, mode };
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
  const items = parseFeedXml(text, src.id, "wechat" as any).map((x) => toItem(x, "wechat"));
  return { items, collector: "wechat_gateway", provider: provider || "primary", mode: env.mode ?? "live" };
}

export function connectorEnvFromProcess(): ConnectorEnv {
  const e = (k: string) => process.env[k]?.trim() || undefined;
  return { telegramWorkerUrl: e("TELEGRAM_WORKER_URL"), wechatGatewayUrl: e("WECHAT_GATEWAY_URL"), wechatGatewayToken: e("WECHAT_GATEWAY_TOKEN") };
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
