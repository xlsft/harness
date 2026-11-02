import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

/**
 * Runtime configuration, read lazily from process.env so tests can set env vars
 * before the first access. Call `resetConfig()` to re-read.
 */
export interface Config {
  dataDir: string;
  dbUrl: string;
  secret: string;
  publicUrl: string;
  promptsDir: string;
  adminEmail?: string;
  adminPassword?: string;
  allowSignup: boolean;
  upstreamMaxRetries: number;
  upstreamRetryBaseMs: number;
  pingIntervalMs: number;
}

let cached: Config | undefined;

export function config(): Config {
  if (cached) return cached;
  const env = process.env;
  const dataDir = resolve(env.HARNESS_DATA_DIR || "./.data");
  mkdirSync(dataDir, { recursive: true });
  cached = {
    dataDir,
    dbUrl: env.HARNESS_DB_URL || `file:${resolve(dataDir, "harness.db")}`,
    secret: loadSecret(dataDir, env.HARNESS_SECRET),
    publicUrl: (env.HARNESS_PUBLIC_URL || `http://localhost:${env.PORT || 3000}`).replace(/\/+$/, ""),
    promptsDir: resolve(env.HARNESS_PROMPTS_DIR || "./prompts"),
    adminEmail: env.HARNESS_ADMIN_EMAIL || undefined,
    adminPassword: env.HARNESS_ADMIN_PASSWORD || undefined,
    allowSignup: env.HARNESS_ALLOW_SIGNUP === "true",
    upstreamMaxRetries: intEnv(env.HARNESS_UPSTREAM_MAX_RETRIES, 8),
    upstreamRetryBaseMs: intEnv(env.HARNESS_UPSTREAM_RETRY_BASE_MS, 500),
    pingIntervalMs: intEnv(env.HARNESS_PING_INTERVAL_MS, 10_000),
  };
  return cached;
}

export function resetConfig(): void {
  cached = undefined;
}

function intEnv(v: string | undefined, def: number): number {
  if (v === undefined || v === "") return def;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) && n >= 0 ? n : def;
}

/**
 * HARNESS_SECRET is the master key (Better Auth secret and provider-key encryption are
 * derived from it). If it is not set we generate one into the data dir so `pnpm dev`
 * works out of the box; production deployments must set it explicitly.
 */
function loadSecret(dataDir: string, fromEnv: string | undefined): string {
  if (fromEnv) {
    if (fromEnv.length < 32) throw new Error("HARNESS_SECRET must be at least 32 characters");
    return fromEnv;
  }
  const file = resolve(dataDir, "secret");
  if (existsSync(file)) return readFileSync(file, "utf8").trim();
  const secret = randomBytes(32).toString("base64url");
  writeFileSync(file, secret + "\n", { mode: 0o600 });
  console.warn(`[harness] HARNESS_SECRET is not set; generated one at ${file}. Set it explicitly in production.`);
  return secret;
}
