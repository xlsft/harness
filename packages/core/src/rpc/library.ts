/**
 * Connect implementation of harness.library.v1: artifacts, Knowledge and transcript search (boards: boards.ts),
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
import { KnowledgeService, KnowledgeSourceKind } from "../gen/harness/library/v1/knowledge_pb.ts";
import { TranscriptSearchService } from "../gen/harness/library/v1/transcripts_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { deleteArtifact, getArtifact, listArtifacts, publishArtifact, updateArtifact, type ArtifactKindName, type ArtifactView } from "../services/artifacts.ts";
import {
  createKnowledgeDocument,
  deleteKnowledgeDocument,
  getKnowledgeDocument,
  listKnowledgeDocuments,
  browseKnowledgeSource,
  listKnowledgeSources,
  searchKnowledge,
  updateKnowledgeDocument,
  type KnowledgeDocumentView,
  type KnowledgeScope,
  type SourceRef,
} from "../services/knowledge.ts";
import { searchTranscripts } from "../services/sessions.ts";
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
  throw new ApiError("invalid_request", "Knowledge has only the user and org scopes");
}

function scopeToProto(s: string): Scope {
  return s === "org" ? Scope.ORG : Scope.USER;
}

function sourceToProto(s: SourceRef) {
  return { id: s.id, kind: s.kind === "provider" ? KnowledgeSourceKind.PROVIDER : KnowledgeSourceKind.INTERNAL, scope: scopeToProto(s.scope), name: s.name, provider: s.provider };
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
    source: sourceToProto(d.source),
    url: d.url,
    hasChildren: d.hasChildren,
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
      const r = await searchKnowledge(p, { query: req.query, limit: req.limit, scope: scopeFromProto(req.scope) });
      return {
        hits: r.hits.map((h) => ({ documentId: h.document_id, title: h.title, scope: scopeToProto(h.scope), snippet: h.snippet, score: h.score, source: sourceToProto(h.source), url: h.url })),
        warnings: r.warnings.map((w) => ({ source: sourceToProto(w.source), message: w.message })),
      };
    }),
    listKnowledgeSources: guard(async (_req, ctx: HandlerContext) => {
      const sources = await listKnowledgeSources(await requirePrincipal(ctx));
      return { sources: sources.map((s) => ({ ref: sourceToProto(s.ref), documentCount: s.documentCount, writable: s.writable })) };
    }),
    browseKnowledgeSource: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await browseKnowledgeSource(p, { sourceId: req.sourceId, parentId: req.parentId, limit: req.limit, offset: req.offset });
      return { documents: r.documents.map(documentToProto), hasMore: r.hasMore };
    }),
  });

  router.service(TranscriptSearchService, {
    searchTranscripts: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { hits: await searchTranscripts(p, req.query, req.limit) };
    }),
  });
}
