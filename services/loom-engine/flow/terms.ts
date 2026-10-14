// §7.3 Chinese industry term dictionary + aliases for retrieval.
// Built-in groups live in terms/zh-industry.json (data, not code); a workspace can add its own
// aliases (flow_term_aliases). Matching stays lexical: the query is expanded into equivalent
// variants and any variant may match. CJK terms match as substrings; Latin terms/acronyms only
// as whole words, so "AI" never expands inside "said" or "Taiwan".
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

export type TermGroup = { domain?: string; terms: string[] };
const file = JSON.parse(readFileSync(join(dirname(fileURLToPath(import.meta.url)), "terms", "zh-industry.json"), "utf8"));
export const BUILTIN_TERMS_VERSION: string = file.version;
export const BUILTIN_TERM_GROUPS: TermGroup[] = file.groups;

const isLatin = (t: string) => /^[\x20-\x7e]+$/.test(t);
const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
/** Whole-word regex for Latin terms; null for CJK (substring). */
export const termPattern = (t: string) => (isLatin(t) ? new RegExp(`(^|[^A-Za-z0-9])${esc(t)}(?=$|[^A-Za-z0-9])`, "i") : null);

function occurs(query: string, term: string): boolean {
  const re = termPattern(term);
  return re ? re.test(query) : query.toLowerCase().includes(term.toLowerCase());
}
function replaceTerm(query: string, term: string, by: string): string {
  const re = termPattern(term);
  if (re) return query.replace(new RegExp(re.source, "gi"), (_m, pre) => `${pre}${by}`);
  const i = query.toLowerCase().indexOf(term.toLowerCase());
  return i < 0 ? query : query.slice(0, i) + by + query.slice(i + term.length);
}

export type Expansion = { variants: string[]; matched: { term: string; aliases: string[] }[] };

/** Expands a query with every group whose term occurs in it; capped to keep SQL bounded. */
export function expandQuery(query: string, groups: TermGroup[] = BUILTIN_TERM_GROUPS, cap = 12): Expansion {
  const q = query.trim();
  const variants = [q];
  const matched: Expansion["matched"] = [];
  // Longest terms first so "国家药监局" wins over "药监局".
  const cands: { term: string; group: string[] }[] = [];
  for (const g of groups) for (const t of g.terms) if (t.trim()) cands.push({ term: t.trim(), group: g.terms.map((x) => x.trim()).filter(Boolean) });
  cands.sort((a, b) => b.term.length - a.term.length);
  const consumed: string[] = [];
  for (const c of cands) {
    if (!occurs(q, c.term)) continue;
    if (consumed.some((x) => x.toLowerCase().includes(c.term.toLowerCase()) && !isLatin(c.term))) continue;
    consumed.push(c.term);
    const aliases = c.group.filter((t) => t.toLowerCase() !== c.term.toLowerCase());
    matched.push({ term: c.term, aliases });
    for (const a of aliases) {
      const v = replaceTerm(q, c.term, a);
      if (!variants.some((x) => x.toLowerCase() === v.toLowerCase())) variants.push(v);
      if (variants.length >= cap) return { variants, matched };
    }
  }
  return { variants, matched };
}

/** Builds groups from workspace alias rows (term → alias) merged onto the built-in dictionary. */
export function mergeAliases(rows: { term: string; alias: string }[], base: TermGroup[] = BUILTIN_TERM_GROUPS): TermGroup[] {
  const out = base.map((g) => ({ ...g, terms: [...g.terms] }));
  for (const r of rows) {
    const g = out.find((x) => x.terms.some((t) => t.toLowerCase() === r.term.toLowerCase() || t.toLowerCase() === r.alias.toLowerCase()));
    if (g) { for (const t of [r.term, r.alias]) if (!g.terms.some((x) => x.toLowerCase() === t.toLowerCase())) g.terms.push(t); }
    else out.push({ domain: "workspace", terms: [r.term, r.alias] });
  }
  return out;
}
