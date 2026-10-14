import { createHash } from "node:crypto";
import type { FetchIo } from "./http.ts";
import { publicFetch } from "./outbound-policy.ts";
import { parseFeedXml, RSS_PARSER_VERSION } from "./rss-parser.ts";
import type { NormalizedItem } from "./collectors/types.ts";

export { parseFeedXml } from "./rss-parser.ts";

const UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";

export type FeedValidators = { etag: string; last_modified: string; identity: string };
export type FeedFetch = { items: NormalizedItem[]; not_modified: boolean; rejected_entries: number; validators: FeedValidators };

/** Validators are only reusable for the same URL, platform and parser semantics. */
export function feedValidatorIdentity(feedUrl: string, platform: string): string {
  return createHash("sha256").update(JSON.stringify([feedUrl, platform, RSS_PARSER_VERSION])).digest("hex");
}

/**
 * Fetch and parse a feed (loom collection-reliability: per-entry isolation, final-URL
 * relative links, conditional GET). Passing `previous` validators sends
 * If-None-Match / If-Modified-Since; a 304 yields no items and keeps the validators.
 */
export async function fetchFeed(
  feedUrl: string, sourceId: string, platform: string,
  previous: Partial<FeedValidators> | null = null, io: FetchIo = {},
): Promise<FeedFetch> {
  const identity = feedValidatorIdentity(feedUrl, platform);
  const reusable = previous?.identity === identity;
  const etag = reusable ? previous?.etag ?? "" : "";
  const lastModified = reusable ? previous?.last_modified ?? "" : "";
  const result = await publicFetch(feedUrl, {
    timeoutMs: 12000, resolver: io.resolver, transport: io.transport, maxBytes: io.maxBytes,
    allowNotModified: Boolean(etag || lastModified),
    headers: {
      "User-Agent": UA,
      Accept: "application/rss+xml, application/atom+xml, application/xml, text/html;q=0.9, */*;q=0.8",
      "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
      ...(etag ? { "If-None-Match": etag } : {}),
      ...(lastModified ? { "If-Modified-Since": lastModified } : {}),
    },
  });
  if (result.status === 304) {
    return { items: [], not_modified: true, rejected_entries: 0, validators: { etag: result.headers.etag ?? etag, last_modified: result.headers["last-modified"] ?? lastModified, identity } };
  }
  const stats = { rejected_entries: 0 };
  const items = parseFeedXml(result.text ?? "", sourceId, platform as import("@/lib/platforms").PlatformId, { feedUrl: result.url, stats });
  return { items, not_modified: false, rejected_entries: stats.rejected_entries,
    validators: { etag: result.headers.etag ?? "", last_modified: result.headers["last-modified"] ?? "", identity } };
}

/**
 * Fetch and parse a public RSS/Atom feed (unconditional).
 *
 * `io` is a test seam only (see `FetchIo`); production callers omit it and get
 * the real resolver/transport under the outbound policy.
 */
export async function fetchRss(
  feedUrl: string,
  sourceId: string,
  platform: string,
  io: FetchIo = {},
): Promise<NormalizedItem[]> {
  return (await fetchFeed(feedUrl, sourceId, platform, null, io)).items;
}
