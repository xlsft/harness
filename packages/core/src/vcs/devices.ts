/**
 * Device providers (gh, glab): clients report which CLIs they have (ReportDeviceProviders) and upload the
 * PR / CI state they polled (ReportDeviceSnapshot). A snapshot is diffed against the stored state of each
 * PR, and the transitions become VcsEvents with source GH / GLAB, linked to sessions like webhook events.
 * Repositories on a host that a server connection covers are ignored: the server provider wins.
 */
import { and, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import { CheckState, ProviderType, VcsEvent_Kind } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "../services/devices.ts";
import { allConnections } from "./connections.ts";
import { checksOf, getStoredPr, prOf, savePr, type EventInput } from "./events.ts";
import { publishEvents } from "./pipeline.ts";
import type { CheckData, ChecksSummaryData, PrData } from "./types.ts";
import { hostOfBaseUrl, isOpen, isoTime, parseRepoKey, summarize } from "./util.ts";

const DEVICE_TYPES = new Set<number>([ProviderType.GH, ProviderType.GLAB]);
const MAX_PRS = 200;
const MAX_CHECKS = 300;

export interface DeviceProviderInput {
  type: number;
  version: string;
  hosts: { host: string; login: string }[];
}

export async function reportDeviceProviders(p: Principal, providers: DeviceProviderInput[]): Promise<void> {
  if (providers.length > 10) throw new ApiError("invalid_request", "At most 10 providers");
  for (const pr of providers) {
    if (!DEVICE_TYPES.has(pr.type)) throw new ApiError("invalid_request", "Device providers are PROVIDER_TYPE_GH or PROVIDER_TYPE_GLAB");
    if (pr.hosts.length > 50) throw new ApiError("invalid_request", "At most 50 hosts per provider");
  }
  await ensureDb();
  const deviceId = p.deviceId ?? "";
  const now = new Date();
  await getDb().transaction(async (tx) => {
    await tx.delete(schema.vcsDeviceProvider).where(and(eq(schema.vcsDeviceProvider.userId, p.user.id), eq(schema.vcsDeviceProvider.deviceId, deviceId)));
    for (const pr of providers) {
      await tx.insert(schema.vcsDeviceProvider).values({
        userId: p.user.id,
        deviceId,
        type: pr.type,
        version: pr.version.slice(0, 200),
        hosts: pr.hosts.slice(0, 50).map((h) => ({ host: h.host.toLowerCase().slice(0, 200), login: h.login.slice(0, 200) })),
        reportedAt: now,
      }).onConflictDoNothing();
    }
  });
}

export async function listDeviceProviders(userId: string) {
  await ensureDb();
  return getDb().select().from(schema.vcsDeviceProvider).where(eq(schema.vcsDeviceProvider.userId, userId));
}

export interface SnapshotInput {
  provider: number;
  pullRequests: PrData[];
  /** Keyed by "repository_key#number". */
  checks: Record<string, CheckData[]>;
  observedAt: string;
}

function sameChecksTerminal(s: ChecksSummaryData | undefined): boolean {
  return !!s && s.total > 0 && s.running === 0;
}

/** Diffs one PR against its previous state; returns the events (without source/actor). */
export function diffPr(prev: PrData | undefined, prevChecks: CheckData[], pr: PrData, checks: CheckData[]): { kind: number; detail: Record<string, unknown> }[] {
  const out: { kind: number; detail: Record<string, unknown> }[] = [];
  if (!prev) {
    if (isOpen(pr)) out.push({ kind: VcsEvent_Kind.PR_OPENED, detail: { first_seen: true } });
  } else if (prev.state !== pr.state) {
    if (pr.state === PullRequest_State.MERGED) out.push({ kind: VcsEvent_Kind.PR_MERGED, detail: {} });
    else if (pr.state === PullRequest_State.CLOSED) out.push({ kind: VcsEvent_Kind.PR_CLOSED, detail: {} });
    else if (!isOpen(prev)) out.push({ kind: VcsEvent_Kind.PR_OPENED, detail: { reopened: true } });
    else out.push({ kind: VcsEvent_Kind.PR_UPDATED, detail: { draft: pr.draft } });
  } else if (prev.title !== pr.title || prev.headSha !== pr.headSha || prev.baseBranch !== pr.baseBranch || prev.autoMergeEnabled !== pr.autoMergeEnabled) {
    out.push({ kind: VcsEvent_Kind.PR_UPDATED, detail: {} });
  }
  if (prev && prev.reviewState !== pr.reviewState && (pr.reviewState === "approved" || pr.reviewState === "changes_requested")) {
    out.push({ kind: VcsEvent_Kind.REVIEW_SUBMITTED, detail: { state: pr.reviewState } });
  }
  if (pr.hasConflicts && !prev?.hasConflicts && isOpen(pr)) {
    out.push({ kind: VcsEvent_Kind.CONFLICT_DETECTED, detail: { base_branch: pr.baseBranch, head_branch: pr.headBranch } });
  }
  const before = new Map(prevChecks.map((c) => [c.id, c]));
  const newlyFailed = checks.filter((c) => c.state === CheckState.FAILURE && before.get(c.id)?.state !== CheckState.FAILURE);
  if (newlyFailed.length) {
    out.push({ kind: VcsEvent_Kind.CHECK_FAILED, detail: { sha: pr.headSha ?? "", checks: newlyFailed.map((c) => ({ id: c.id, name: c.name, url: c.url, summary: c.summary })) } });
  }
  const nowSummary = checks.length ? summarize(checks) : pr.checks;
  const prevSummary = prevChecks.length ? summarize(prevChecks) : prev?.checks;
  if (sameChecksTerminal(nowSummary) && (!sameChecksTerminal(prevSummary) || prev?.headSha !== pr.headSha) && (prevChecks.length || prev?.checks)) {
    out.push({ kind: VcsEvent_Kind.CHECKS_COMPLETED, detail: { sha: pr.headSha ?? "", summary: nowSummary } });
  }
  return out;
}

export async function reportDeviceSnapshot(p: Principal, input: SnapshotInput): Promise<number> {
  if (!DEVICE_TYPES.has(input.provider)) throw new ApiError("invalid_request", "provider must be PROVIDER_TYPE_GH or PROVIDER_TYPE_GLAB");
  if (input.pullRequests.length > MAX_PRS) throw new ApiError("invalid_request", `At most ${MAX_PRS} pull requests per snapshot`);
  await ensureDb();
  const covered = new Set((await allConnections()).map((c) => hostOfBaseUrl(c.baseUrl)));
  const checksByKey = new Map<string, CheckData[]>();
  for (const [k, list] of Object.entries(input.checks)) {
    const i = k.lastIndexOf("#");
    if (i <= 0) continue;
    try {
      checksByKey.set(`${parseRepoKey(k.slice(0, i)).key}#${Number(k.slice(i + 1))}`, list.slice(0, MAX_CHECKS));
    } catch {
      // ignore malformed keys
    }
  }
  const events: EventInput[] = [];
  const observed = isoTime(input.observedAt) || new Date().toISOString();
  for (const raw of input.pullRequests) {
    const repo = parseRepoKey(raw.repositoryKey);
    if (!raw.number || raw.number < 0) throw new ApiError("invalid_request", "pull request number is required");
    // Server wins: the server's webhooks and API are the source of truth for covered hosts.
    if (covered.has(repo.host)) continue;
    const checks = checksByKey.get(`${repo.key}#${raw.number}`);
    const pr: PrData = { ...raw, repositoryKey: repo.key, checks: checks?.length ? summarize(checks) : raw.checks };
    const row = await getStoredPr(repo.key, pr.number);
    if (row && (row.source === ProviderType.GITHUB || row.source === ProviderType.GITLAB)) continue;
    const prev = row ? prOf(row) : undefined;
    // headSha is not part of the proto message: a changed updated_at / checks set stands in for a new head.
    pr.headSha = pr.headSha ?? prev?.headSha ?? "";
    const prevChecks = checksOf(row);
    for (const d of diffPr(prev, prevChecks, pr, checks ?? [])) {
      events.push({ kind: d.kind, source: input.provider, repositoryKey: repo.key, pr, detail: d.detail, occurredAt: observed, actorUserId: p.user.id });
    }
    await savePr(pr, { source: input.provider, checks: checks ?? prevChecks, reportedBy: p.user.id });
  }
  const rows = await publishEvents(events);
  return rows.length;
}
