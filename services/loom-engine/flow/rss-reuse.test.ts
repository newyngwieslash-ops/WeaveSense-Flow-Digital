import { test } from "node:test";
import assert from "node:assert/strict";
import { parseFeedXml } from "../src/lib/server/rss-parser.ts";

test("reuses WeaveSense RSS/Atom parser", () => {
  const rss = `<?xml version="1.0"?><rss><channel><item><guid>g1</guid><title>Hello</title><link>https://e.com/1</link><description>Body</description><pubDate>Mon, 12 Oct 2026 01:00:00 GMT</pubDate></item></channel></rss>`;
  const items = parseFeedXml(rss, "src_1", "rss" as any);
  assert.equal(items.length, 1);
  assert.equal(items[0].external_id, "g1");
  assert.equal(items[0].title, "Hello");
});
