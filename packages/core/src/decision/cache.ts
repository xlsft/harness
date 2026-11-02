/**
 * In-memory LRU of classifier decisions, so repeating the same call in the same context does not pay for
 * another model call. Keyed by user, provider, model, tool, normalized input, working directories and a
 * hash of the recent user messages (the intent the call is judged against). Only ALLOW and DENY are
 * cached; ASK means "not sure" and is worth asking again. Per process; not shared between replicas.
 */
import { createHash } from "node:crypto";
import type { DecideInput, ProviderDecision } from "./types.ts";

interface Entry {
  value: ProviderDecision;
  expiresAt: number;
}

export class DecisionCache {
  private readonly map = new Map<string, Entry>();

  constructor(
    readonly maxEntries = 5_000,
    private readonly now: () => number = Date.now,
  ) {}

  get(key: string): ProviderDecision | undefined {
    const e = this.map.get(key);
    if (!e) return undefined;
    if (e.expiresAt <= this.now()) {
      this.map.delete(key);
      return undefined;
    }
    // refresh recency
    this.map.delete(key);
    this.map.set(key, e);
    return e.value;
  }

  set(key: string, value: ProviderDecision, ttlMs: number): void {
    if (ttlMs <= 0 || value.decision === "ask") return;
    this.map.delete(key);
    this.map.set(key, { value, expiresAt: this.now() + ttlMs });
    while (this.map.size > this.maxEntries) {
      const oldest = this.map.keys().next().value;
      if (oldest === undefined) break;
      this.map.delete(oldest);
    }
  }

  get size(): number {
    return this.map.size;
  }

  clear(): void {
    this.map.clear();
  }
}

/** JSON with sorted object keys, so equal inputs give equal keys regardless of key order. */
export function stableStringify(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(stableStringify).join(",")}]`;
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    return `{${Object.keys(o)
      .sort()
      .filter((k) => o[k] !== undefined)
      .map((k) => `${JSON.stringify(k)}:${stableStringify(o[k])}`)
      .join(",")}}`;
  }
  return JSON.stringify(v ?? null);
}

/** Tool input without cosmetic fields (Bash `description`), whitespace-normalized command. */
export function normalizeInput(toolName: string, input: Record<string, unknown>): string {
  const copy: Record<string, unknown> = { ...input };
  if (toolName === "Bash") {
    delete copy.description;
    if (typeof copy.command === "string") copy.command = copy.command.trim();
  }
  return stableStringify(copy);
}

const sha = (s: string) => createHash("sha256").update(s).digest("hex");

export function decisionCacheKey(userId: string, providerId: string, modelId: string, input: DecideInput): string {
  const intent = sha(input.recentUserMessages.join("\u0000"));
  return sha(
    stableStringify([
      userId,
      providerId,
      modelId,
      input.toolName,
      normalizeInput(input.toolName, input.input),
      input.cwd,
      [...input.additionalDirectories].sort(),
      intent,
    ]),
  );
}

const caches = new Map<string, DecisionCache>();

/** Process-wide cache per provider id; survives configuration reloads. */
export function cacheFor(providerId: string): DecisionCache {
  let c = caches.get(providerId);
  if (!c) {
    c = new DecisionCache();
    caches.set(providerId, c);
  }
  return c;
}

/** For tests. */
export function clearDecisionCaches(): void {
  for (const c of caches.values()) c.clear();
}
