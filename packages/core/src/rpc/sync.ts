/** Connect implementation of harness.sync.v1.SyncService: proto <-> services/sync.ts, services/sessions.ts. */
import type { DescMessage, MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import {
  SyncService,
  type BatchPutResultSchema,
  type ChangeSchema,
  type ConflictSchema,
  type DocumentRef as PbDocumentRef,
  type DocumentSchema,
  type PutDocumentRequest,
  type SessionMetadataSchema,
  type TombstoneSchema,
  type TranscriptEntrySchema,
  type WatchChangesResponseSchema,
} from "../gen/harness/sync/v1/sync_pb.ts";
import { AsyncQueue } from "../lib/async.ts";
import { ApiError, asApiError } from "../lib/errors.ts";
import {
  appendSessionEvents,
  deleteSession,
  getSession,
  getSessionTranscript,
  listSessions,
  upsertSession,
  type EntryRow,
  type SessionView,
} from "../services/sessions.ts";
import {
  currentPublishedHead,
  deleteDocument,
  feedChange,
  getDocument,
  listChanges,
  MAX_BATCH,
  publishedHead,
  putDocument,
  rowFiles,
  subscribe,
  type Change,
  type DocRef,
  type DocRow,
  type PutResult,
} from "../services/sync.ts";
import type { Principal } from "../services/devices.ts";
import { requirePrincipal } from "./context.ts";
import { errorCode, guard, toConnectError } from "./errors.ts";

type Init<T extends DescMessage> = MessageInitShape<T>;

function docToProto(r: DocRow): Init<typeof DocumentSchema> {
  return {
    id: r.id,
    scope: r.scope,
    kind: r.kind,
    key: r.key,
    version: BigInt(r.version),
    content: r.content,
    files: rowFiles(r),
    contentHash: r.contentHash,
    source: r.source,
    targetRoles: Array.isArray(r.targetRoles) ? (r.targetRoles as string[]) : [],
    createdAt: r.createdAt.toISOString(),
    updatedAt: r.updatedAt.toISOString(),
    updatedBy: r.updatedBy,
    projectKey: r.projectKey,
  };
}

function tombstoneToProto(r: DocRow): Init<typeof TombstoneSchema> {
  return {
    id: r.id,
    scope: r.scope,
    kind: r.kind,
    key: r.key,
    version: BigInt(r.version),
    deletedAt: (r.deletedAt ?? r.updatedAt).toISOString(),
    projectKey: r.projectKey,
  };
}

function changeToProto(c: Change): Init<typeof ChangeSchema> {
  return "document" in c
    ? { change: { case: "document", value: docToProto(c.document) } }
    : { change: { case: "tombstone", value: tombstoneToProto(c.tombstone) } };
}

function conflictToProto(r: DocRow | null): Init<typeof ConflictSchema> {
  if (!r) return {};
  return r.deletedAt ? { current: { case: "tombstone", value: tombstoneToProto(r) } } : { current: { case: "document", value: docToProto(r) } };
}

function refFromProto(ref: PbDocumentRef | undefined): DocRef | undefined {
  if (!ref) return undefined;
  return { scope: ref.scope, kind: ref.kind, key: ref.key, projectKey: ref.projectKey };
}

function putInput(req: PutDocumentRequest) {
  return {
    ref: refFromProto(req.ref)!,
    content: req.content,
    files: req.files.map((f) => ({ path: f.path, content: f.content, executable: f.executable })),
    baseVersion: req.baseVersion === undefined ? undefined : Number(req.baseVersion),
    source: req.source,
    targetRoles: req.targetRoles,
  };
}

function putResultToProto(r: PutResult) {
  return "document" in r
    ? { result: { case: "document" as const, value: docToProto(r.document) } }
    : { result: { case: "conflict" as const, value: conflictToProto(r.conflict) } };
}

function sessionToProto(s: SessionView): Init<typeof SessionMetadataSchema> {
  return {
    info: s.info,
    promptBundleVersion: s.promptBundleVersion,
    lastSeq: BigInt(s.lastSeq),
    createdAt: s.createdAt,
    lastActivityAt: s.lastActivityAt,
  };
}

function entryToProto(e: EntryRow): Init<typeof TranscriptEntrySchema> {
  return { uuid: e.uuid, parentUuid: e.parentUuid, type: e.type, timestamp: e.timestamp, entryJson: e.entryJson, seq: BigInt(e.seq), agentId: e.agentId };
}

/** Heartbeat interval of WatchChanges (HARNESS_SYNC_HEARTBEAT_MS, default 20 s). */
function heartbeatMs(): number {
  const n = Number(process.env.HARNESS_SYNC_HEARTBEAT_MS ?? "");
  return Number.isFinite(n) && n > 0 ? Math.min(n, 20_000) : 20_000;
}

async function* watch(p: Principal, sinceVersion: number, kinds: number[], signal: AbortSignal): AsyncGenerator<Init<typeof WatchChangesResponseSchema>> {
  // Subscribe before the catch-up so nothing committed in between is missed; duplicates are skipped by version.
  const queue = new AsyncQueue<DocRow>();
  const unsubscribe = subscribe((row) => queue.push(row));
  try {
    await publishedHead();
    let cursor = sinceVersion;
    let token: string | undefined;
    for (;;) {
      // Continuations never ask for a resync, so a long catch-up cannot fail half-way.
      const page = await listChanges(p, cursor, kinds, 1000, token);
      if (page.resyncRequired) {
        yield { event: { case: "resyncRequired", value: {} } };
        return;
      }
      for (const c of page.changes) yield { event: { case: "change", value: changeToProto(c) } };
      cursor = page.nextVersion;
      token = page.nextPageToken || undefined;
      if (!page.hasMore) break;
    }
    yield { event: { case: "caughtUp", value: { version: BigInt(cursor) } } };

    const interval = heartbeatMs();
    let lastSent = Date.now();
    while (!signal.aborted) {
      const row = await queue.next(Math.max(0, interval - (Date.now() - lastSent)), signal);
      if (signal.aborted) return;
      if (!row) {
        // The queue is drained, so every published change up to the head was already delivered.
        cursor = Math.max(cursor, currentPublishedHead() ?? cursor);
        yield { event: { case: "heartbeat", value: { version: BigInt(cursor) } } };
        lastSent = Date.now();
        continue;
      }
      if (row.version <= cursor) continue;
      cursor = row.version;
      const c = feedChange(row, p.user, kinds);
      if (!c) continue;
      yield { event: { case: "change", value: changeToProto(c) } };
      lastSent = Date.now();
    }
  } finally {
    unsubscribe();
  }
}

export function registerSyncService(router: ConnectRouter): void {
  router.service(SyncService, {
    listChanges: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const page = await listChanges(p, Number(req.sinceVersion), req.kinds, req.pageSize, req.pageToken || undefined);
      return {
        changes: page.changes.map(changeToProto),
        nextVersion: BigInt(page.nextVersion),
        hasMore: page.hasMore,
        resyncRequired: page.resyncRequired,
        nextPageToken: page.nextPageToken,
      };
    }),

    async *watchChanges(req, ctx: HandlerContext): AsyncGenerator<Init<typeof WatchChangesResponseSchema>> {
      try {
        const p = await requirePrincipal(ctx);
        yield* watch(p, Number(req.sinceVersion), req.kinds, ctx.signal);
      } catch (err) {
        throw toConnectError(err);
      }
    },

    getDocument: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const sel = req.selector;
      if (sel.case === "id") return { document: docToProto(await getDocument(p, { id: sel.value })) };
      if (sel.case === "ref") return { document: docToProto(await getDocument(p, { ref: refFromProto(sel.value)! })) };
      throw new ApiError("invalid_request", "id or ref is required");
    }),

    putDocument: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return putResultToProto(await putDocument(p, putInput(req)));
    }),

    batchPutDocuments: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      if (req.items.length > MAX_BATCH) throw new ApiError("invalid_request", `At most ${MAX_BATCH} items per batch`);
      const results: Init<typeof BatchPutResultSchema>[] = [];
      for (const item of req.items) {
        try {
          results.push(putResultToProto(await putDocument(p, putInput(item))));
        } catch (err) {
          const e = asApiError(err);
          results.push({ result: { case: "error", value: { code: errorCode(e.type), message: e.message } } });
        }
      }
      return { results };
    }),

    deleteDocument: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      if (req.baseVersion !== undefined && req.baseVersion === 0n) throw new ApiError("invalid_request", "base_version 0 is not meaningful for deletes");
      const r = await deleteDocument(p, req.id, req.baseVersion === undefined ? undefined : Number(req.baseVersion));
      return "tombstone" in r
        ? { result: { case: "tombstone" as const, value: tombstoneToProto(r.tombstone) } }
        : { result: { case: "conflict" as const, value: conflictToProto(r.conflict) } };
    }),

    upsertSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const s = req.session;
      const view = await upsertSession(p, { info: s?.info, promptBundleVersion: s?.promptBundleVersion ?? "" }, req.updatePaths);
      return { session: sessionToProto(view) };
    }),

    appendSessionEvents: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await appendSessionEvents(
        p,
        req.sessionId,
        req.entries.map((e) => ({ uuid: e.uuid, parentUuid: e.parentUuid, type: e.type, timestamp: e.timestamp, entryJson: e.entryJson, agentId: e.agentId })),
      );
      return { accepted: r.accepted, duplicates: r.duplicates, lastSeq: BigInt(r.lastSeq) };
    }),

    getSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { session: sessionToProto(await getSession(p, req.sessionId)) };
    }),

    listSessions: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listSessions(p, {
        repo: req.repo,
        cwd: req.cwd,
        deviceId: req.deviceId,
        statuses: req.statuses,
        query: req.query,
        includeArchived: req.includeArchived,
        pageSize: req.pageSize,
        pageToken: req.pageToken,
      });
      return { sessions: r.sessions.map(sessionToProto), nextPageToken: r.nextPageToken };
    }),

    getSessionTranscript: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await getSessionTranscript(p, req.sessionId, Number(req.afterSeq), req.pageSize, req.agentId);
      return { entries: r.entries.map(entryToProto), hasMore: r.hasMore };
    }),

    deleteSession: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await deleteSession(p, req.sessionId);
      return {};
    }),
  });
}

