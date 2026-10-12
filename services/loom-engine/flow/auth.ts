// Verifies/signs execution-context tokens (HS256), mirroring backend/internal/flow/token.go.
import { createHmac, timingSafeEqual, randomBytes } from "node:crypto";

export interface ExecutionContext {
  iss: string; aud: string; iat: number; exp: number; jti: string;
  tenant_id: string; workspace_id: string;
  actor: { type: string; id: string };
  operation_id?: string; job_id?: string; scopes: string[];
  budget_reservation_id?: string; policy_version: string;
}
const HEADER = Buffer.from('{"alg":"HS256","typ":"JWT"}').toString("base64url");
const MAX_TTL = 300;

export class AuthError extends Error { status = 401; }

export function verify(key: string, token: string, audience: string, nowSec = Math.floor(Date.now() / 1000)): ExecutionContext {
  const parts = token.split(".");
  if (parts.length !== 3 || parts[0] !== HEADER) throw new AuthError("invalid token");
  const sig = createHmac("sha256", key).update(parts[0] + "." + parts[1]).digest();
  const got = Buffer.from(parts[2], "base64url");
  if (got.length !== sig.length || !timingSafeEqual(got, sig)) throw new AuthError("bad signature");
  let c: ExecutionContext;
  try { c = JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8")); } catch { throw new AuthError("bad body"); }
  if (c.aud !== audience || !c.tenant_id || !c.workspace_id) throw new AuthError("bad audience/scope");
  if (nowSec >= c.exp || c.exp - c.iat > MAX_TTL) throw new AuthError("expired");
  return c;
}

export function sign(key: string, c: Omit<ExecutionContext, "iat" | "exp" | "jti">, nowSec = Math.floor(Date.now() / 1000), ttl = MAX_TTL): string {
  if (Buffer.byteLength(key) < 32) throw new Error("signing key must be >= 32 bytes");
  const body = { ...c, iat: nowSec, exp: nowSec + Math.min(ttl, MAX_TTL), jti: "jti_" + randomBytes(12).toString("hex") };
  const unsigned = HEADER + "." + Buffer.from(JSON.stringify(body)).toString("base64url");
  return unsigned + "." + createHmac("sha256", key).update(unsigned).digest("base64url");
}

export function requireScope(c: ExecutionContext, scope: string) {
  if (!c.scopes?.includes(scope)) { const e = new AuthError("missing scope " + scope); e.status = 403; throw e; }
}
