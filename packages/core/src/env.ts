import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

/**
 * Runtime configuration, read lazily from process.env so tests can set env vars
 * before the first access. Call `resetConfig()` to re-read.
 */
export interface Config {
  dataDir: string;
  db: DbConfig;
  secret: string;
  publicUrl: string;
  promptsDir: string;
  adminEmail?: string;
  adminPassword?: string;
  adminUsername?: string;
  allowSignup: boolean;
  upstreamMaxRetries: number;
  upstreamRetryBaseMs: number;
  pingIntervalMs: number;
  /** An upstream that sends nothing (no headers, no bytes) for this long is dropped and the attempt retried. */
  upstreamIdleTimeoutMs: number;
  /** The model price catalog (services/price-catalog.ts); HARNESS_MODEL_PRICES=off for air-gapped installs. */
  modelPricesEnabled: boolean;
  /** Where it is fetched from. An operator's own URL (a mirror) may be on the private network. */
  modelPricesUrl: string;
}

export const DEFAULT_MODEL_PRICES_URL = "https://models.dev/api.json";

/**
 * Where the database lives. `postgres`: a real server (DATABASE_URL, a normal URL or a unix socket such as
 * postgres:///harness?host=/run/postgresql). `pglite`: embedded Postgres (WASM) in `dataDir`, or in memory
 * when dataDir is "memory://".
 */
export type DbConfig = { driver: "postgres"; url: string } | { driver: "pglite"; dataDir: string };

let cached: Config | undefined;

export function config(): Config {
  if (cached) return cached;
  const env = process.env;
  const dataDir = resolve(env.HARNESS_DATA_DIR || "./.data");
  mkdirSync(dataDir, { recursive: true });
  cached = {
    dataDir,
    db: dbConfig(env.DATABASE_URL, dataDir),
    secret: loadSecret(dataDir, env.HARNESS_SECRET),
    publicUrl: (env.HARNESS_PUBLIC_URL || `http://localhost:${env.PORT || 3000}`).replace(/\/+$/, ""),
    promptsDir: resolve(env.HARNESS_PROMPTS_DIR || "./prompts"),
    adminEmail: env.HARNESS_ADMIN_EMAIL || undefined,
    adminPassword: env.HARNESS_ADMIN_PASSWORD || undefined,
    adminUsername: env.HARNESS_ADMIN_USERNAME || undefined,
    allowSignup: env.HARNESS_ALLOW_SIGNUP === "true",
    upstreamMaxRetries: intEnv(env.HARNESS_UPSTREAM_MAX_RETRIES, 8),
    upstreamRetryBaseMs: intEnv(env.HARNESS_UPSTREAM_RETRY_BASE_MS, 500),
    pingIntervalMs: intEnv(env.HARNESS_PING_INTERVAL_MS, 10_000),
    upstreamIdleTimeoutMs: intEnv(env.HARNESS_UPSTREAM_IDLE_TIMEOUT_MS, 180_000),
    modelPricesEnabled: !["off", "false", "0"].includes((env.HARNESS_MODEL_PRICES ?? "").trim().toLowerCase()),
    modelPricesUrl: env.HARNESS_MODEL_PRICES_URL?.trim() || DEFAULT_MODEL_PRICES_URL,
  };
  return cached;
}

export function resetConfig(): void {
  cached = undefined;
}

/** DATABASE_URL unset: PGlite persisted to $HARNESS_DATA_DIR/pglite. "memory://": in-memory PGlite (tests). */
function dbConfig(url: string | undefined, dataDir: string): DbConfig {
  if (!url) return { driver: "pglite", dataDir: resolve(dataDir, "pglite") };
  if (url === "memory://" || url === "memory:") return { driver: "pglite", dataDir: "memory://" };
  if (/^postgres(ql)?:\/\//.test(url)) return { driver: "postgres", url };
  throw new Error("DATABASE_URL must be a postgres:// or postgresql:// URL (or memory:// for an in-memory PGlite)");
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
