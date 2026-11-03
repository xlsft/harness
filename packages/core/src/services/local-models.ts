/**
 * Local models: LLM servers on users' machines (Ollama, LM Studio, ...) that clients call directly
 * (proto/harness/agent/v1/localmodels.proto). The server never reaches them and keeps none of their
 * configuration (that stays on each device); it only
 *  - holds the admin's policy whether clients may use them (AdminService.Get/SetLocalModelPolicy), in the org
 *    POLICY document "default" as `disableLocalModels: true`, which clients read from their sync feed;
 *  - stores the token usage clients report for them (UsageService.ReportLocalUsage), at cost 0;
 *  - builds prompt bundles for their ids ("local:<provider>/<name>"), which are in no catalog.
 */
import { ensureDb, getDb, schema } from "../db/index.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "./devices.ts";
import { getDocument, putDocument, type DocRef, type DocRow } from "./sync.ts";

export const LOCAL_MODEL_PREFIX = "local:";
export const POLICY_KEY = "disableLocalModels";

const POLICY_REF: DocRef = { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default", projectKey: "" };

type Json = Record<string, unknown>;

export function isLocalModelId(id: string): boolean {
  return id.startsWith(LOCAL_MODEL_PREFIX) && /^local:[^/]+\/.+$/.test(id);
}

/** Whether a POLICY document's content allows local models (the default). */
export function localModelsAllowedIn(content: Json): boolean {
  const v = content[POLICY_KEY];
  return !(v === true || v === "disable");
}

/** Content of the policy document with the local model switch set, everything else kept. */
export function withLocalModelsAllowed(content: Json, allowed: boolean): Json {
  const next: Json = { ...content };
  if (allowed) delete next[POLICY_KEY];
  else next[POLICY_KEY] = true;
  return next;
}

async function readPolicy(p: Principal): Promise<DocRow | undefined> {
  try {
    return await getDocument(p, { ref: POLICY_REF });
  } catch (e) {
    if (e instanceof ApiError && e.type === "not_found") return undefined;
    throw e;
  }
}

function parse(content: string | undefined): Json {
  if (!content) return {};
  try {
    const v: unknown = JSON.parse(content);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Json) : {};
  } catch {
    return {};
  }
}

export async function getLocalModelPolicy(p: Principal): Promise<boolean> {
  return localModelsAllowedIn(parse((await readPolicy(p))?.content));
}

/** Sets the switch (admins only; the caller checks the role). Retries a concurrent write. */
export async function setLocalModelPolicy(p: Principal, allowed: boolean): Promise<boolean> {
  for (let attempt = 0; attempt < 5; attempt++) {
    const row = await readPolicy(p);
    const r = await putDocument(p, {
      ref: POLICY_REF,
      content: JSON.stringify(withLocalModelsAllowed(parse(row?.content), allowed)),
      files: [],
      baseVersion: row?.version ?? 0,
      source: "admin:local-model-policy",
      targetRoles: (row?.targetRoles as string[] | undefined) ?? [],
    });
    if ("document" in r) return localModelsAllowedIn(parse(r.document.content));
  }
  throw new ApiError("conflict", "The policy changed concurrently; try again");
}

// --- Usage ----------------------------------------------------------------------------------------------

export interface LocalUsageInput {
  id: string;
  model_id: string;
  provider_id: string;
  request_class: string;
  session_id: string;
  agent_id: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  status: string;
  stop_reason: string;
  duration_ms: number;
  created_at: string;
}

export const MAX_LOCAL_USAGE_BATCH = 500;
const MAX_TOKENS = 100_000_000;
const STATUSES = new Set(["ok", "error", "aborted"]);

function clampInt(n: number, max = MAX_TOKENS): number {
  return Number.isFinite(n) && n > 0 ? Math.min(Math.floor(n), max) : 0;
}

function short(s: string, max = 200): string | null {
  const t = s.trim();
  return t ? t.slice(0, max) : null;
}

/**
 * Stores usage of local models reported by the caller's client: cost 0, priced (free, not unknown), under the
 * caller's device. Records are deduplicated by their client id (per user). Returns how many were stored.
 */
export async function recordLocalUsage(p: Principal, records: LocalUsageInput[], now = new Date()): Promise<number> {
  if (records.length > MAX_LOCAL_USAGE_BATCH) throw new ApiError("invalid_request", `At most ${MAX_LOCAL_USAGE_BATCH} records per call`);
  for (const r of records) {
    if (!isLocalModelId(r.model_id)) throw new ApiError("invalid_request", `Not a local model id: ${JSON.stringify(r.model_id)}`);
  }
  if (!records.length) return 0;
  await ensureDb();
  const rows = records.map((r) => {
    const at = r.created_at ? new Date(r.created_at) : now;
    const providerId = r.provider_id || r.model_id.slice(LOCAL_MODEL_PREFIX.length).split("/")[0]!;
    // Client ids are scoped to the user so one user cannot shadow another's records.
    const clientId = /^[A-Za-z0-9_-]{8,80}$/.test(r.id) ? r.id : "";
    return {
      id: clientId ? `lus_${p.user.id}_${clientId}` : newId("lus"),
      userId: p.user.id,
      deviceId: p.deviceId ?? null,
      modelId: r.model_id.slice(0, 300),
      providerId: `${LOCAL_MODEL_PREFIX}${providerId}`.slice(0, 120),
      upstreamModel: r.model_id.slice(r.model_id.indexOf("/") + 1).slice(0, 300),
      requestClass: short(r.request_class, 40),
      sessionId: short(r.session_id),
      agentId: short(r.agent_id),
      inputTokens: clampInt(r.input_tokens),
      outputTokens: clampInt(r.output_tokens),
      cacheReadTokens: clampInt(r.cache_read_tokens),
      cacheWriteTokens: 0,
      costUsd: 0,
      priced: true,
      retries: 0,
      status: STATUSES.has(r.status) ? r.status : "ok",
      stopReason: short(r.stop_reason, 40),
      errorType: null,
      durationMs: clampInt(r.duration_ms, 2_147_483_647),
      createdAt: Number.isNaN(at.getTime()) || at > now ? now : at,
    };
  });
  const inserted = await getDb()
    .insert(schema.usageRecord)
    .values(rows)
    .onConflictDoNothing({ target: schema.usageRecord.id })
    .returning({ id: schema.usageRecord.id });
  return inserted.length;
}
