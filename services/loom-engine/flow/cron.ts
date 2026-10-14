// Shared 5-field cron evaluation (briefs and collection schedules).
import { HttpError } from "./errors.ts";

// ---------- cron (5 fields: min hour dom mon dow) evaluated in an IANA timezone ----------
function field(spec: string, min: number, max: number): Set<number> {
  const out = new Set<number>();
  for (const part of spec.split(",")) {
    const [range, stepS] = part.split("/");
    const step = stepS ? Number(stepS) : 1;
    let lo = min, hi = max;
    if (range !== "*") {
      const [a, b] = range.split("-").map(Number);
      lo = a; hi = b ?? (stepS ? max : a);
    }
    if (![lo, hi, step].every(Number.isInteger) || lo < min || hi > max || lo > hi || step < 1) throw new HttpError(400, `invalid cron field "${spec}"`);
    for (let v = lo; v <= hi; v += step) out.add(v);
  }
  return out;
}
export function parseCron(expr: string) {
  const f = String(expr ?? "").trim().split(/\s+/);
  if (f.length !== 5) throw new HttpError(400, "cron must have 5 fields");
  const dow = field(f[4], 0, 7);
  if (dow.has(7)) dow.add(0);
  return { min: field(f[0], 0, 59), hour: field(f[1], 0, 23), dom: field(f[2], 1, 31), mon: field(f[3], 1, 12), dow, domAny: f[2] === "*", dowAny: f[4] === "*" };
}
export function checkTimezone(tz: string) {
  try { new Intl.DateTimeFormat("en-US", { timeZone: tz }); } catch { throw new HttpError(400, "invalid timezone"); }
}
/** Next fire time strictly after `after`, scanning minute by minute (≤ 62 days). */
export function nextCron(expr: string, tz: string, after: Date): Date {
  const c = parseCron(expr); checkTimezone(tz);
  const fmt = new Intl.DateTimeFormat("en-US", { timeZone: tz, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", weekday: "short" });
  const wd: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };
  let t = Math.floor(after.getTime() / 60000) * 60000 + 60000;
  for (let i = 0; i < 62 * 1440; i++, t += 60000) {
    const p = Object.fromEntries(fmt.formatToParts(new Date(t)).map((x) => [x.type, x.value]));
    const dom = c.dom.has(Number(p.day)), dow = c.dow.has(wd[p.weekday]);
    const dayOk = c.domAny || c.dowAny ? dom && dow : dom || dow;
    if (c.min.has(Number(p.minute)) && c.hour.has(Number(p.hour)) && c.mon.has(Number(p.month)) && dayOk) return new Date(t);
  }
  throw new HttpError(400, "cron never fires within 62 days");
}

