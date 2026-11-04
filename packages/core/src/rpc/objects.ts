/**
 * Connect implementation of harness.objects.v1.ObjectsService: proto <-> src/services/objects.ts. Reads go through the
 * HTTP route (src/http/routes/object.ts), which has what a stream of bytes needs: ranges and safe content types.
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { ObjectKind, ObjectsService, type ObjectUse as ObjectUseProto } from "../gen/harness/objects/v1/objects_pb.ts";
import { ApiError } from "../lib/errors.ts";
import {
  deleteObject,
  finishUpload,
  listSessionObjects,
  objectLimits,
  startUpload,
  statObject,
  uploadChunk,
  type ObjectKindName,
  type ObjectRow,
  type ObjectUse,
} from "../services/objects.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

const KIND_NAME: Partial<Record<ObjectKind, ObjectKindName>> = {
  [ObjectKind.ATTACHMENT]: "attachment",
  [ObjectKind.SCRATCH]: "scratch",
  [ObjectKind.TOOL_RESULT]: "tool_result",
  [ObjectKind.IMAGE]: "image",
  [ObjectKind.SENT_FILE]: "sent_file",
};

const KIND_PROTO: Record<ObjectKindName, ObjectKind> = {
  attachment: ObjectKind.ATTACHMENT,
  avatar: ObjectKind.AVATAR,
  scratch: ObjectKind.SCRATCH,
  tool_result: ObjectKind.TOOL_RESULT,
  image: ObjectKind.IMAGE,
  sent_file: ObjectKind.SENT_FILE,
};

/** Avatars go through UpdateMe, which checks that they are images of a sensible size. */
function useFromProto(u: ObjectUseProto | undefined): ObjectUse {
  const kind = u && KIND_NAME[u.kind];
  if (!kind) throw new ApiError("invalid_request", "use.kind is required (and not an avatar)");
  return { session_id: u.sessionId, kind, name: u.name, content_type: u.contentType };
}

function refToProto(r: ObjectRow) {
  return { sha256: r.sha256, size: BigInt(r.size), mediaType: r.contentType, name: r.name };
}

function recordToProto(r: ObjectRow) {
  return { ref: refToProto(r), sessionId: r.sessionId, kind: KIND_PROTO[r.kind as ObjectKindName] ?? ObjectKind.UNSPECIFIED, createdAt: r.createdAt.toISOString(), updatedAt: r.updatedAt.toISOString() };
}

export function registerObjectsService(router: ConnectRouter): void {
  router.service(ObjectsService, {
    startUpload: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await startUpload(p, { sha256: req.sha256, size: Number(req.size), use: useFromProto(req.use) });
      if (r.already_stored) return { alreadyStored: true, record: recordToProto(r.record), chunkSize: BigInt(r.chunk_size) };
      return { uploadId: r.upload_id, chunkSize: BigInt(r.chunk_size), receivedChunks: r.received_chunks };
    }),

    uploadChunk: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await uploadChunk(p, { upload_id: req.uploadId, index: req.index, data: req.data });
      return {};
    }),

    finishUpload: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { record: recordToProto(await finishUpload(p, { upload_id: req.uploadId, use: useFromProto(req.use) })) };
    }),

    statObject: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { ref: refToProto(await statObject(p, req.sha256)) };
    }),

    listSessionObjects: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const r = await listSessionObjects(p, { session_id: req.sessionId, page_size: req.pageSize, page_token: req.pageToken });
      return { records: r.records.map(recordToProto), nextPageToken: r.next_page_token };
    }),

    deleteObject: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      await deleteObject(p, { sha256: req.sha256, session_id: req.sessionId });
      return {};
    }),

    getObjectLimits: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const l = await objectLimits(p);
      return { maxFileBytes: BigInt(l.max_file_bytes), quotaBytes: BigInt(l.quota_bytes), usedBytes: BigInt(l.used_bytes), chunkSize: BigInt(l.chunk_size) };
    }),
  });
}
