/** Shared helpers of the VCS providers: repository keys, check summaries, review state, log tails, HTTP. */
import type { Response as UndiciResponse } from "undici";
import { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import { CheckState } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import type { CheckData, ChecksSummaryData, PrData, RepoRef } from "./types.ts";

// --- repository keys ---------------------------------------------------------------------------

/**
 * "host/owner/repo", lowercased, without scheme, user info, port and a trailing ".git" — the same
 * normalization as project_key (proto/harness/common/v1/scope.proto).
 */
export function normalizeRepoKey(host: string, path: string): string {
  const h = host.toLowerCase().replace(/:\d+$/, "").replace(/^\[|\]$/g, "");
  const p = path
    .toLowerCase()
    .replace(/^\/+|\/+$/g, "")
    .replace(/\.git$/, "");
  return `${h}/${p}`;
}

/** Repository key of a web URL such as https://github.com/Owner/Repo or https://host:8443/group/sub/proj.git. */
export function repoKeyFromUrl(url: string): string {
  const u = new URL(url);
  return normalizeRepoKey(u.hostname, u.pathname);
}

const KEY_RE = /^[a-z0-9][a-z0-9.-]*(\/[^\s/]+){2,}$/;

/** Parses and normalizes a repository key; throws invalid_request when it is not "host/owner/repo". */
export function parseRepoKey(raw: string): RepoRef {
  if (!raw) throw new ApiError("invalid_request", "repository_key is required");
  const trimmed = raw.trim().replace(/^[a-z]+:\/\//i, "");
  const slash = trimmed.indexOf("/");
  const key = slash > 0 ? normalizeRepoKey(trimmed.slice(0, slash), trimmed.slice(slash + 1)) : trimmed.toLowerCase();
  if (key.length > 300 || !KEY_RE.test(key) || key.split("/").some((s) => s === "." || s === "..")) {
    throw new ApiError("invalid_request", 'repository_key must be "host/owner/repo" (lowercased, no scheme)');
  }
  const parts = key.split("/");
  const path = parts.slice(1).join("/");
  return { key, host: parts[0]!, path, owner: parts[1]!, name: parts[parts.length - 1]! };
}

export function hostOfBaseUrl(baseUrl: string): string {
  return new URL(baseUrl).hostname.toLowerCase().replace(/^\[|\]$/g, "");
}

// --- checks --------------------------------------------------------------------------------------

export function summarize(checks: CheckData[]): ChecksSummaryData {
  let passed = 0;
  let failed = 0;
  let running = 0;
  let cancelled = 0;
  for (const c of checks) {
    if (c.state === CheckState.SUCCESS) passed++;
    else if (c.state === CheckState.FAILURE) failed++;
    else if (c.state === CheckState.RUNNING || c.state === CheckState.QUEUED) running++;
    else if (c.state === CheckState.CANCELLED) cancelled++;
  }
  let state = CheckState.UNSPECIFIED;
  if (checks.length) {
    if (failed) state = CheckState.FAILURE;
    else if (running) state = CheckState.RUNNING;
    else if (cancelled) state = CheckState.CANCELLED;
    else if (passed) state = CheckState.SUCCESS;
    else state = CheckState.NEUTRAL;
  }
  return { state, total: checks.length, passed, failed, running };
}

export function durationMs(start: string | null | undefined, end: string | null | undefined): number {
  if (!start) return 0;
  const a = Date.parse(start);
  const b = end ? Date.parse(end) : Date.now();
  return Number.isFinite(a) && Number.isFinite(b) && b > a ? b - a : 0;
}

export function upsertCheck(list: CheckData[], c: CheckData): CheckData[] {
  const out = list.filter((x) => x.id !== c.id && !(x.name === c.name && x.workflow === c.workflow && x.id.split(":")[0] === c.id.split(":")[0]));
  out.push(c);
  return out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

// --- pull requests -------------------------------------------------------------------------------

/** Aggregates per-reviewer review states: any changes_requested wins over approved. */
export function aggregateReviews(reviewers: Record<string, string>, reviewRequested: boolean): string {
  const states = Object.values(reviewers);
  if (states.includes("changes_requested")) return "changes_requested";
  if (states.includes("approved")) return "approved";
  return reviewRequested ? "review_required" : "none";
}

export function isOpen(pr: Pick<PrData, "state">): boolean {
  return pr.state === PullRequest_State.OPEN || pr.state === PullRequest_State.DRAFT;
}

/** RFC 3339 UTC from GitHub/GitLab timestamps (GitLab hooks use "2024-01-02 03:04:05 UTC"). */
export function isoTime(v: unknown): string {
  if (typeof v !== "string" || !v) return "";
  const s = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}( UTC)?$/.test(v) ? v.replace(" UTC", "Z").replace(" ", "T") : v;
  const t = Date.parse(s);
  return Number.isFinite(t) ? new Date(t).toISOString() : "";
}

// --- HTTP ----------------------------------------------------------------------------------------

export const DEFAULT_LOG_BYTES = 64 * 1024;
export const MAX_LOG_BYTES = 1 << 20;
/** Logs are streamed and only the tail is kept; give up after this many bytes. */
const MAX_LOG_STREAM_BYTES = 64 << 20;

export function logLimit(maxBytes: number): number {
  if (!maxBytes || maxBytes <= 0) return DEFAULT_LOG_BYTES;
  return Math.min(maxBytes, MAX_LOG_BYTES);
}

/** Reads a response body keeping only the last `limit` bytes. */
export async function readTail(res: UndiciResponse, limit: number): Promise<{ log: string; truncated: boolean }> {
  if (!res.body) return { log: "", truncated: false };
  const chunks: Buffer[] = [];
  let kept = 0;
  let total = 0;
  for await (const chunk of res.body as AsyncIterable<Uint8Array>) {
    const b = Buffer.from(chunk);
    total += b.length;
    chunks.push(b);
    kept += b.length;
    while (chunks.length > 1 && kept - chunks[0]!.length >= limit) kept -= chunks.shift()!.length;
    if (total > MAX_LOG_STREAM_BYTES) break;
  }
  let buf = Buffer.concat(chunks);
  const truncated = total > buf.length || buf.length > limit;
  if (buf.length > limit) buf = buf.subarray(buf.length - limit);
  // Do not start in the middle of a UTF-8 sequence.
  let start = 0;
  while (truncated && start < buf.length && start < 4 && (buf[start]! & 0xc0) === 0x80) start++;
  return { log: buf.subarray(start).toString("utf8"), truncated };
}

export interface HttpTarget {
  allowPrivateNetwork: boolean;
}

export interface RequestOpts {
  method?: string;
  headers?: Record<string, string>;
  body?: unknown;
  /** Value of the Authorization header. */
  authorization?: string;
  /** Whether the token is the user's (a 401 then means "authorize again"). */
  userToken?: boolean;
  followRedirects?: boolean;
  timeoutMs?: number;
}

export const USER_AGENT = "harness-core";

export async function httpRequest(target: HttpTarget, url: string, o: RequestOpts = {}): Promise<UndiciResponse> {
  const headers: Record<string, string> = { "user-agent": USER_AGENT, ...o.headers };
  if (o.authorization) headers.authorization = o.authorization;
  let body: string | undefined;
  if (o.body !== undefined) {
    if (typeof o.body === "string") body = o.body;
    else {
      body = JSON.stringify(o.body);
      headers["content-type"] ??= "application/json";
    }
  }
  try {
    return await guardedFetch(url, {
      method: o.method ?? "GET",
      headers,
      body,
      allowPrivateNetwork: target.allowPrivateNetwork,
      followRedirects: o.followRedirects,
      signal: AbortSignal.timeout(o.timeoutMs ?? 30_000),
    });
  } catch (err) {
    if (err instanceof ApiError) throw err;
    throw new ApiError("upstream", `Code host request failed: ${err instanceof Error ? err.message : String(err)}`);
  }
}

/** Maps a non-2xx code host response to an ApiError (and consumes the body). */
export async function upstreamError(res: UndiciResponse, what: string, userToken: boolean): Promise<ApiError> {
  let detail = "";
  try {
    const text = await res.text();
    try {
      const j = JSON.parse(text) as { message?: unknown; error?: unknown; error_description?: unknown; errors?: unknown };
      const m = j.message ?? j.error_description ?? j.error;
      detail = typeof m === "string" ? m : m ? JSON.stringify(m) : "";
      if (Array.isArray(j.errors) && j.errors.length) detail += ` ${JSON.stringify(j.errors).slice(0, 300)}`;
    } catch {
      detail = text.slice(0, 300);
    }
  } catch {
    // ignore
  }
  const msg = `${what}: ${res.status}${detail ? ` ${detail}` : ""}`.slice(0, 600);
  const opts = { upstreamStatus: res.status };
  switch (res.status) {
    case 401:
      return userToken ? new ApiError("needs_auth", `${msg} (authorize your account again)`, opts) : new ApiError("upstream", msg, opts);
    case 403:
      if (res.headers.get("x-ratelimit-remaining") === "0") {
        const reset = Number(res.headers.get("x-ratelimit-reset"));
        return new ApiError("rate_limit", msg, { ...opts, retryAfterMs: Number.isFinite(reset) ? Math.max(0, reset * 1000 - Date.now()) : undefined });
      }
      return new ApiError("permission", msg, opts);
    case 404:
      return new ApiError("not_found", msg, opts);
    case 405:
    case 409:
      return new ApiError("failed_precondition", msg, opts);
    case 400:
    case 422:
      return new ApiError("invalid_request", msg, opts);
    case 429:
      return new ApiError("rate_limit", msg, opts);
    default:
      return new ApiError("upstream", msg, opts);
  }
}

export async function httpJson<T>(target: HttpTarget, url: string, what: string, o: RequestOpts = {}): Promise<{ data: T; res: UndiciResponse }> {
  const res = await httpRequest(target, url, o);
  if (!res.ok) throw await upstreamError(res, what, !!o.userToken);
  const text = await res.text();
  let data: T;
  try {
    data = (text ? JSON.parse(text) : null) as T;
  } catch {
    throw new ApiError("upstream", `${what}: invalid JSON from the code host`);
  }
  return { data, res };
}

/** Next page number from a Link header (rel="next"), or "" when there is none. */
export function nextPageFromLink(link: string | null): string {
  if (!link) return "";
  for (const part of link.split(",")) {
    const m = /<([^>]+)>;\s*rel="next"/.exec(part);
    if (m) {
      try {
        return new URL(m[1]!).searchParams.get("page") ?? "";
      } catch {
        return "";
      }
    }
  }
  return "";
}

export function str(v: unknown): string {
  return typeof v === "string" ? v : v === undefined || v === null ? "" : String(v);
}

export function num(v: unknown): number {
  const n = typeof v === "number" ? v : Number(v);
  return Number.isFinite(n) ? n : 0;
}
