/**
 * Connect implementation of harness.library.v1: artifacts, the knowledge base, the task tracker and transcript search,
 * proto <-> the services of the same name. Artifact bytes go through the HTTP route (src/http/routes/artifact.ts).
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import {
  ArtifactKind,
  ArtifactService,
  ArtifactVisibility,
  ListArtifactsRequest_Filter as Filter,
} from "../gen/harness/library/v1/artifacts_pb.ts";
import { KnowledgeService } from "../gen/harness/library/v1/knowledge_pb.ts";
import { TrackerPriority, TrackerService, TrackerStatus } from "../gen/harness/library/v1/tracker_pb.ts";
import { TranscriptSearchService } from "../gen/harness/library/v1/transcripts_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { deleteArtifact, getArtifact, listArtifacts, publishArtifact, updateArtifact, type ArtifactKindName, type ArtifactView } from "../services/artifacts.ts";
import {
  createKnowledgeDocument,
  deleteKnowledgeDocument,
  getKnowledgeDocument,
  listKnowledgeDocuments,
  searchKnowledge,
  updateKnowledgeDocument,
  type KnowledgeDocumentView,
  type KnowledgeScope,
} from "../services/knowledge.ts";
import { searchTranscripts } from "../services/sessions.ts";
import {
  createTrackerTask,
  deleteTrackerTask,
  getTrackerTask,
  listTrackerTasks,
  updateTrackerTask,
  type TrackerPriorityName,
  type TrackerRow,
  type TrackerStatusName,
} from "../services/tracker.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

const KIND: Record<ArtifactKindName, ArtifactKind> = {
  page: ArtifactKind.PAGE,
  markdown: ArtifactKind.MARKDOWN,
  text: ArtifactKind.TEXT,
  image: ArtifactKind.IMAGE,
  pdf: ArtifactKind.PDF,
  file: ArtifactKind.FILE,
};

function versionToProto(v: { number: number; sha256: string; size: number; contentType: string; name: string; createdAt: Date }) {
  return { number: v.number, ref: { sha256: v.sha256, size: BigInt(v.size), mediaType: v.contentType, name: v.name }, createdAt: v.createdAt.toISOString() };
}

function artifactToProto(a: ArtifactView) {
  return {
    id: a.row.id,
    title: a.row.title,
    kind: KIND[a.kind],
    visibility: a.row.visibility === "shared" ? ArtifactVisibility.SHARED : ArtifactVisibility.PRIVATE,
    mine: a.mine,
    ownerName: a.ownerName,
    // A session id means something to its owner alone.
    sessionId: a.mine ? a.row.sessionId : "",
    current: versionToProto(a.version),
    createdAt: a.row.createdAt.toISOString(),
    updatedAt: a.row.updatedAt.toISOString(),
  };
}

function scopeFromProto(s: Scope): KnowledgeScope | undefined {
  if (s === Scope.UNSPECIFIED) return undefined;
  if (s === Scope.USER) return "user";
  if (s === Scope.ORG) return "org";
  throw new ApiError("invalid_request", "The knowledge base has only the user and org scopes");
}

function scopeToProto(s: string): Scope {
  return s === "org" ? Scope.ORG : Scope.USER;
}

function documentToProto(d: KnowledgeDocumentView) {
  return {
    id: d.row.id,
    scope: scopeToProto(d.row.scope),
    title: d.row.title,
    sourceName: d.row.sourceName,
    size: BigInt(d.row.size),
    editable: d.editable,
    createdAt: d.row.createdAt.toISOString(),
    updatedAt: d.row.updatedAt.toISOString(),
  };
}

const STATUS_NAME: Partial<Record<TrackerStatus, TrackerStatusName>> = {
  [TrackerStatus.TODO]: "todo",
  [TrackerStatus.IN_PROGRESS]: "in_progress",
  [TrackerStatus.BLOCKED]: "blocked",
  [TrackerStatus.DONE]: "done",
  [TrackerStatus.CANCELLED]: "cancelled",
};
const STATUS_PROTO: Record<TrackerStatusName, TrackerStatus> = {
  todo: TrackerStatus.TODO,
  in_progress: TrackerStatus.IN_PROGRESS,
  blocked: TrackerStatus.BLOCKED,
  done: TrackerStatus.DONE,
  cancelled: TrackerStatus.CANCELLED,
};
const PRIORITY_NAME: Partial<Record<TrackerPriority, TrackerPriorityName>> = {
  [TrackerPriority.LOW]: "low",
  [TrackerPriority.NORMAL]: "normal",
  [TrackerPriority.HIGH]: "high",
};
const PRIORITY_PROTO: Record<TrackerPriorityName, TrackerPriority> = { low: TrackerPriority.LOW, normal: TrackerPriority.NORMAL, high: TrackerPriority.HIGH };

function taskToProto(t: TrackerRow) {
  return {
    id: t.id,
    title: t.title,
    description: t.description,
    status: STATUS_PROTO[t.status as TrackerStatusName] ?? TrackerStatus.UNSPECIFIED,
    priority: PRIORITY_PROTO[t.priority as TrackerPriorityName] ?? TrackerPriority.UNSPECIFIED,
    project: t.project,
    sessionIds: (t.sessionIds ?? []) as string[],
    blockedBy: (t.blockedBy ?? []) as string[],
    createdAt: t.createdAt.toISOString(),
    updatedAt: t.updatedAt.toISOString(),
    closedAt: t.closedAt ? t.closedAt.toISOString() : "",
  };
}

export function registerLibraryServices(router: ConnectRouter): void {
  router.service(ArtifactService, {
    publishArtifact: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await publishArtifact(p, { artifact_id: req.artifactId, title: req.title, sha256: req.sha256, name: req.name, content_type: req.contentType, session_id: req.sessionId });
      return { artifact: artifactToProto(r.view), unchanged: r.unchanged };
    }),
    listArtifacts: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const filter = req.filter === Filter.MINE ? "mine" : req.filter === Filter.SHARED ? "shared" : "all";
      const r = await listArtifacts(p, { filter, query: req.query, session_id: req.sessionId, page_size: req.pageSize, page_token: req.pageToken });
      return { artifacts: r.artifacts.map(artifactToProto), nextPageToken: r.next_page_token };
    }),
    getArtifact: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await getArtifact(p, req.id, req.version);
      return { artifact: artifactToProto(r.view), versions: r.versions.map(versionToProto), text: r.text };
    }),
    updateArtifact: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      let visibility: "private" | "shared" | undefined;
      if (req.visibility !== undefined) {
        if (req.visibility === ArtifactVisibility.UNSPECIFIED) throw new ApiError("invalid_request", "visibility must be private or shared");
        visibility = req.visibility === ArtifactVisibility.SHARED ? "shared" : "private";
      }
      return { artifact: artifactToProto(await updateArtifact(p, req.id, { title: req.title, visibility })) };
    }),
    deleteArtifact: guard(async (req, ctx: HandlerContext) => {
      await deleteArtifact(await requirePrincipal(ctx), req.id);
      return {};
    }),
  });

  router.service(KnowledgeService, {
    listKnowledgeDocuments: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { documents: (await listKnowledgeDocuments(p, { scope: scopeFromProto(req.scope), query: req.query })).map(documentToProto) };
    }),
    getKnowledgeDocument: guard(async (req, ctx: HandlerContext) => {
      const d = await getKnowledgeDocument(await requirePrincipal(ctx), req.id);
      return { document: documentToProto(d), content: d.row.content };
    }),
    createKnowledgeDocument: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const scope = scopeFromProto(req.scope);
      if (!scope) throw new ApiError("invalid_request", "scope is required");
      return { document: documentToProto(await createKnowledgeDocument(p, { scope, title: req.title, content: req.content, source_name: req.sourceName })) };
    }),
    updateKnowledgeDocument: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { document: documentToProto(await updateKnowledgeDocument(p, req.id, { title: req.title, content: req.content })) };
    }),
    deleteKnowledgeDocument: guard(async (req, ctx: HandlerContext) => {
      await deleteKnowledgeDocument(await requirePrincipal(ctx), req.id);
      return {};
    }),
    searchKnowledge: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const hits = await searchKnowledge(p, { query: req.query, limit: req.limit, scope: scopeFromProto(req.scope) });
      return { hits: hits.map((h) => ({ documentId: h.document_id, title: h.title, scope: scopeToProto(h.scope), snippet: h.snippet, score: h.score })) };
    }),
  });

  const statusOf = (s: TrackerStatus): TrackerStatusName | undefined => {
    if (s === TrackerStatus.UNSPECIFIED) return undefined;
    const name = STATUS_NAME[s];
    if (!name) throw new ApiError("invalid_request", "Unknown status");
    return name;
  };
  const priorityOf = (s: TrackerPriority): TrackerPriorityName | undefined => {
    if (s === TrackerPriority.UNSPECIFIED) return undefined;
    const name = PRIORITY_NAME[s];
    if (!name) throw new ApiError("invalid_request", "Unknown priority");
    return name;
  };

  router.service(TrackerService, {
    listTrackerTasks: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const statuses = req.statuses.map(statusOf).filter((s): s is TrackerStatusName => !!s);
      return { tasks: (await listTrackerTasks(p, { statuses, project: req.project, query: req.query, limit: req.limit })).map(taskToProto) };
    }),
    getTrackerTask: guard(async (req, ctx: HandlerContext) => ({ task: taskToProto(await getTrackerTask(await requirePrincipal(ctx), req.id)) })),
    createTrackerTask: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const task = await createTrackerTask(p, {
        title: req.title,
        description: req.description,
        status: statusOf(req.status),
        priority: priorityOf(req.priority),
        project: req.project,
        session_id: req.sessionId,
        blocked_by: req.blockedBy,
      });
      return { task: taskToProto(task) };
    }),
    updateTrackerTask: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const task = await updateTrackerTask(p, req.id, {
        title: req.title,
        description: req.description,
        status: req.status === undefined ? undefined : statusOf(req.status),
        priority: req.priority === undefined ? undefined : priorityOf(req.priority),
        project: req.project,
        session_id: req.sessionId,
        blocked_by: req.setBlockedBy ? req.blockedBy : undefined,
      });
      return { task: taskToProto(task) };
    }),
    deleteTrackerTask: guard(async (req, ctx: HandlerContext) => {
      await deleteTrackerTask(await requirePrincipal(ctx), req.id);
      return {};
    }),
  });

  router.service(TranscriptSearchService, {
    searchTranscripts: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { hits: await searchTranscripts(p, req.query, req.limit) };
    }),
  });
}
