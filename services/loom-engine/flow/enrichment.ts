// Flow adapters for upstream loom collection-capability enrichment (cdcb787f):
//  - article reader: upstream readArticle() (hosted reader, per-host allowlist,
//    public-DNS rechecks via outbound-policy, access-challenge rejection);
//  - YouTube subtitles: upstream subtitle worker contract (POST /v1/transcript,
//    bearer token, bounded 2 MiB body) parsed with upstream transcriptText().
// Both are opt-in by env exactly like upstream (ARTICLE_READER_ENABLED /
// SUBTITLE_ENABLED). Tenant scoping lives in Engine: callers resolve the
// document under (tenant_id, workspace_id) before any outbound call.
import { createHash } from "node:crypto";
import { youtubeVideoId, transcriptText } from "../src/lib/server/subtitles.ts";
import { subtitleLanguages, type SubtitleLanguage } from "../src/lib/subtitle-contract.ts";

export type ArticleReader = (url: string) => Promise<{ title: string; body: string; sourceUrl: string; digest: string }>;
export type Transcript = { status: "ok" | "no_subtitles"; kind: "manual" | "automatic" | "none"; language: SubtitleLanguage; text: string; digest: string };
export type SubtitleClient = (videoId: string, language: SubtitleLanguage) => Promise<Transcript>;

export class EnrichmentError extends Error {
  code: string;
  constructor(code: string) { super(code); this.code = code; }
}

export function articleHostsFromEnv(raw = process.env.ARTICLE_READER_ALLOWED_HOSTS ?? ""): Set<string> {
  const hosts = raw.split(",").map((h) => h.trim().toLowerCase()).filter(Boolean);
  // Same rule as upstream: one malformed or wildcard entry disables the allowlist entirely.
  if (hosts.some((h) => { try { return new URL(`https://${h}`).hostname !== h || h.includes("*"); } catch { return true; } })) return new Set();
  return new Set(hosts);
}

export const defaultArticleReader: ArticleReader = async (url) => {
  if (process.env.ARTICLE_READER_ENABLED !== "true") throw new EnrichmentError("article_disabled");
  const { readArticle } = await import("../src/lib/server/article-reader.ts");
  const a = await readArticle(url, { allowedHosts: articleHostsFromEnv() });
  return { title: a.title, body: a.body, sourceUrl: a.sourceUrl, digest: a.digest };
};

/** Video id for a Flow document; reuses the upstream URL/external-id rules. */
export function documentVideoId(d: { url: string; external_id?: string | null }): string {
  try { return youtubeVideoId({ platform: "youtube", url: d.url, external_id: d.external_id ?? undefined }); }
  catch { throw new EnrichmentError("subtitle_unsupported"); }
}

export function isSubtitleLanguage(x: unknown): x is SubtitleLanguage { return (subtitleLanguages as readonly unknown[]).includes(x); }

const MAX_BYTES = 2 * 1024 * 1024;

export function subtitleClient(opts: { endpoint?: string; token?: string; enabled?: boolean; fetchImpl?: typeof fetch } = {}): SubtitleClient {
  return async (videoId, language) => {
    const enabled = opts.enabled ?? process.env.SUBTITLE_ENABLED === "true";
    const token = opts.token ?? process.env.SUBTITLE_SERVICE_TOKEN ?? "";
    const raw = opts.endpoint ?? process.env.SUBTITLE_URL ?? "http://subtitle-egress:8794";
    if (!enabled) throw new EnrichmentError("subtitle_disabled");
    let endpoint: URL;
    try { endpoint = new URL(raw); } catch { throw new EnrichmentError("subtitle_unavailable"); }
    if (!token || !["http:", "https:"].includes(endpoint.protocol) || endpoint.username || endpoint.password || endpoint.search || endpoint.hash || endpoint.pathname !== "/") throw new EnrichmentError("subtitle_unavailable");
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 50_000);
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    try {
      const res = await (opts.fetchImpl ?? fetch)(new URL("/v1/transcript", endpoint), { method: "POST", redirect: "error", signal: controller.signal,
        headers: { "content-type": "application/json", authorization: `Bearer ${token}` }, body: JSON.stringify({ video_id: videoId, language }) });
      if (!res.body) throw new EnrichmentError("subtitle_invalid_response");
      reader = res.body.getReader();
      const chunks: Uint8Array[] = []; let size = 0;
      for (;;) {
        const n = await reader.read(); if (n.done) break;
        size += n.value.byteLength; if (size > MAX_BYTES) throw new EnrichmentError("subtitle_output_too_large"); chunks.push(n.value);
      }
      let data: any;
      try { data = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks))); } catch { throw new EnrichmentError("subtitle_invalid_response"); }
      if (!res.ok) throw new EnrichmentError(["timeout", "busy", "output_too_large"].includes(data?.error) ? `subtitle_${data.error}` : "subtitle_unavailable");
      if (!data || data.video_id !== videoId || data.language !== language) throw new EnrichmentError("subtitle_invalid_response");
      if (data.status === "no_subtitles" && data.kind === "none") return { status: "no_subtitles", kind: "none", language, text: "", digest: "" };
      if (data.status !== "ok" || !["manual", "automatic"].includes(data.kind) || typeof data.json3 !== "string") throw new EnrichmentError("subtitle_invalid_response");
      let text: string;
      try { text = transcriptText(data.json3); } catch (e: any) { throw new EnrichmentError(String(e?.message ?? "subtitle_invalid_response")); }
      if (data.text !== text) throw new EnrichmentError("subtitle_invalid_response");
      return { status: "ok", kind: data.kind, language, text, digest: createHash("sha256").update(data.json3, "utf8").digest("hex") };
    } catch (e) {
      if (e instanceof EnrichmentError) throw e;
      throw new EnrichmentError(controller.signal.aborted ? "subtitle_timeout" : "subtitle_unavailable");
    } finally { clearTimeout(timer); controller.abort(); await reader?.cancel().catch(() => {}); }
  };
}

/** HTTP status for an enrichment failure: config/unsupported → 422, upstream trouble → 502. */
export function enrichmentStatus(code: string): number {
  if (/_(disabled|unsupported|target_not_allowed|wechat_gateway_required|access_challenge|invalid_input)$/.test(code)) return 422;
  if (/_busy$/.test(code)) return 429;
  return 502;
}

// ---------- OCR (§6.4): optional extract-profile worker (tesseract) behind a bearer token ----------
export type OcrResult = { text: string; pages: number; digest: string };
export type OcrClient = (objectKey: string, scope: { tenant_id: string; workspace_id: string }) => Promise<OcrResult>;

/** OCR worker contract: POST /v1/ocr {object_key, tenant_id, workspace_id} → {object_key, text, pages, sha256}. Opt-in (OCR_ENABLED). */
export function ocrClient(opts: { endpoint?: string; token?: string; enabled?: boolean; fetchImpl?: typeof fetch; timeoutMs?: number } = {}): OcrClient {
  return async (objectKey, scope) => {
    const enabled = opts.enabled ?? process.env.OCR_ENABLED === "true";
    const token = opts.token ?? process.env.OCR_SERVICE_TOKEN ?? "";
    const raw = opts.endpoint ?? process.env.OCR_URL ?? "http://ocr-worker:8796";
    if (!enabled) throw new EnrichmentError("ocr_disabled");
    let endpoint: URL;
    try { endpoint = new URL(raw); } catch { throw new EnrichmentError("ocr_unavailable"); }
    if (!token || !["http:", "https:"].includes(endpoint.protocol) || endpoint.username || endpoint.password || endpoint.pathname !== "/") throw new EnrichmentError("ocr_unavailable");
    let res: Response;
    try {
      res = await (opts.fetchImpl ?? fetch)(new URL("/v1/ocr", endpoint), { method: "POST", redirect: "error", signal: AbortSignal.timeout(opts.timeoutMs ?? 120_000),
        headers: { "content-type": "application/json", authorization: `Bearer ${token}` }, body: JSON.stringify({ object_key: objectKey, tenant_id: scope.tenant_id, workspace_id: scope.workspace_id }) });
    } catch (e: any) { throw new EnrichmentError(e?.name === "TimeoutError" ? "ocr_timeout" : "ocr_unavailable"); }
    const text = await res.text();
    if (text.length > MAX_BYTES) throw new EnrichmentError("ocr_output_too_large");
    let data: any; try { data = JSON.parse(text); } catch { throw new EnrichmentError("ocr_invalid_response"); }
    if (!res.ok) throw new EnrichmentError(["unsupported", "busy", "timeout", "not_found"].includes(data?.error) ? `ocr_${data.error === "not_found" ? "unsupported" : data.error}` : "ocr_unavailable");
    if (data?.object_key !== objectKey || typeof data.text !== "string" || !Number.isInteger(data.pages) || data.pages < 0) throw new EnrichmentError("ocr_invalid_response");
    return { text: data.text.slice(0, 200_000), pages: data.pages, digest: typeof data.sha256 === "string" ? data.sha256 : createHash("sha256").update(data.text).digest("hex") };
  };
}
