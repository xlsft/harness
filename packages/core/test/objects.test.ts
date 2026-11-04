/**
 * The object store end to end, over the real routes. Runs against FsStore, the dev fallback behind the same interface,
 * and also against a real S3 server when HARNESS_TEST_S3_ENDPOINT, _ACCESS_KEY and _SECRET_KEY are set (the bundled
 * Garage, see deploy/README.md). CI has no S3 binary, so the signing has a known-answer test of its own.
 */
import { createHash } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { ObjectKind, ObjectsService } from "../src/gen/harness/objects/v1/objects_pb.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const sha = (b: Uint8Array | string) => createHash("sha256").update(b).digest("hex");

describe("sigV4Authorization", () => {
  it("signs the example of the AWS documentation", async () => {
    const { sigV4Authorization } = await import("../src/services/blobstore.ts");
    const auth = sigV4Authorization({
      method: "GET",
      path: "/test.txt",
      query: "",
      headers: {
        host: "examplebucket.s3.amazonaws.com",
        range: "bytes=0-9",
        "x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        "x-amz-date": "20130524T000000Z",
      },
      payloadHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      amzDate: "20130524T000000Z",
      region: "us-east-1",
      accessKey: "AKIAIOSFODNN7EXAMPLE",
      secretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
    });
    expect(auth).toContain("Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41");
    expect(auth).toContain("SignedHeaders=host;range;x-amz-content-sha256;x-amz-date");
  });
});

describe("what the read route sends", () => {
  it("renders only raster images and PDFs, and never HTML or SVG", async () => {
    const { servedHeaders } = await import("../src/services/objects.ts");
    const of = (contentType: string, name = "f") => servedHeaders({ contentType, name, sha256: "a".repeat(64) });
    for (const t of ["image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"]) {
      expect(of(t)["content-type"]).toBe(t);
      expect(of(t)["content-disposition"]).toMatch(/^inline/);
    }
    for (const t of ["image/svg+xml", "text/html", "TEXT/HTML; charset=utf-8", "application/xhtml+xml", "text/javascript", "text/xml", "application/x-evil", ""]) {
      expect(of(t)["content-type"]).toBe("application/octet-stream");
      expect(of(t)["content-disposition"]).toMatch(/^attachment/);
    }
    // A video plays in a <video> but does not open as a page.
    expect(of("video/mp4")["content-type"]).toBe("video/mp4");
    expect(of("video/mp4")["content-disposition"]).toMatch(/^attachment/);
    const h = of("image/png");
    expect(h["x-content-type-options"]).toBe("nosniff");
    expect(h["content-security-policy"]).toMatch(/^sandbox/);
    // No way out of the header.
    expect(of("text/plain", 'a"\r\nb/é.txt')["content-disposition"]).toBe(`attachment; filename="a___b__.txt"; filename*=UTF-8''a___b_%C3%A9.txt`);
  });

  it("parses ranges", async () => {
    const { parseRange } = await import("../src/services/objects.ts");
    expect(parseRange(null, 10)).toBeUndefined();
    expect(parseRange("bytes=0-3", 10)).toEqual({ start: 0, end: 3 });
    expect(parseRange("bytes=4-", 10)).toEqual({ start: 4, end: 9 });
    expect(parseRange("bytes=-2", 10)).toEqual({ start: 8, end: 9 });
    expect(parseRange("bytes=5-100", 10)).toEqual({ start: 5, end: 9 });
    expect(parseRange("bytes=10-", 10)).toBe("invalid");
    expect(parseRange("bytes=0-1,3-4", 10)).toBeUndefined();
    expect(parseRange("chars=0-1", 10)).toBeUndefined();
  });
});

const real = process.env.HARNESS_TEST_S3_ENDPOINT;
const backends: { name: string; env: Record<string, string> }[] = [{ name: "files", env: { HARNESS_S3_ENDPOINT: "" } }];
if (real) {
  backends.push({
    name: "S3",
    env: {
      HARNESS_S3_ENDPOINT: real,
      HARNESS_S3_ACCESS_KEY: process.env.HARNESS_TEST_S3_ACCESS_KEY ?? "",
      HARNESS_S3_SECRET_KEY: process.env.HARNESS_TEST_S3_SECRET_KEY ?? "",
      HARNESS_S3_BUCKET: process.env.HARNESS_TEST_S3_BUCKET ?? "harness-objects",
      HARNESS_S3_REGION: process.env.HARNESS_TEST_S3_REGION ?? "garage",
    },
  });
}

describe.each(backends)("object store on $name", ({ env }) => {
  let srv: TestServer;
  let token: string;
  let other: string;
  let api: Client<typeof ObjectsService>;
  const objects = (t: string) =>
    createClient(
      ObjectsService,
      createConnectTransport({
        baseUrl: srv.url,
        httpVersion: "1.1",
        interceptors: [
          (next) => async (req) => {
            req.header.set("authorization", `Bearer ${t}`);
            return next(req);
          },
        ],
      }),
    );
  const use = (over: Partial<{ sessionId: string; kind: ObjectKind; name: string; contentType: string }> = {}) => ({
    sessionId: "ses_a",
    kind: ObjectKind.SCRATCH,
    name: "file.bin",
    contentType: "application/octet-stream",
    ...over,
  });
  const get = (s: string, t: string, headers: Record<string, string> = {}, method = "GET") =>
    fetch(`${srv.url}/api/objects/${s}`, { method, headers: { authorization: `Bearer ${t}`, ...headers } });

  async function upload(t: string, bytes: Uint8Array, u = use()) {
    const c = objects(t);
    const sum = sha(bytes);
    const s = await c.startUpload({ sha256: sum, size: BigInt(bytes.length), use: u });
    if (s.alreadyStored) return { sum, deduped: true };
    const n = Number(s.chunkSize);
    for (let i = 0; i * n < bytes.length; i++) {
      if (s.receivedChunks.includes(i)) continue;
      await c.uploadChunk({ uploadId: s.uploadId, index: i, data: bytes.subarray(i * n, (i + 1) * n) });
    }
    await c.finishUpload({ uploadId: s.uploadId, use: u });
    return { sum, deduped: false };
  }

  beforeAll(async () => {
    const { resetBlobStore } = await import("../src/services/blobstore.ts");
    const { resetConfig } = await import("../src/env.ts");
    resetBlobStore();
    resetConfig();
    srv = await startTestServer(env);
    const admin = await deviceLogin(srv);
    token = admin.accessToken;
    other = (await createUserAndLogin(srv, admin.cookie, "other@test.local")).accessToken;
    api = objects(token);
  });
  afterAll(async () => {
    await srv.close();
  });

  it("stores a small file, reads it back and serves ranges", async () => {
    const bytes = new TextEncoder().encode("hello object store");
    const { sum } = await upload(token, bytes, use({ contentType: "text/plain", name: "hello.txt" }));

    const all = await get(sum, token);
    expect(all.status).toBe(200);
    expect(await all.text()).toBe("hello object store");
    expect(all.headers.get("content-type")).toBe("text/plain");
    expect(all.headers.get("content-disposition")).toContain("hello.txt");
    expect(all.headers.get("x-content-type-options")).toBe("nosniff");
    expect(all.headers.get("accept-ranges")).toBe("bytes");

    const part = await get(sum, token, { range: "bytes=6-11" });
    expect(part.status).toBe(206);
    expect(await part.text()).toBe("object");
    expect(part.headers.get("content-range")).toBe(`bytes 6-11/${bytes.length}`);

    expect((await get(sum, token, { range: "bytes=100-" })).status).toBe(416);
    expect((await get(sum, token, { "if-none-match": `"${sum}"` })).status).toBe(304);
    const head = await get(sum, token, {}, "HEAD");
    expect(head.status).toBe(200);
    expect(head.headers.get("content-length")).toBe(String(bytes.length));
  });

  it("uploads in chunks, resumes, and refuses bytes that do not match", async () => {
    const bytes = new Uint8Array(20 << 20).map((_, i) => i % 251);
    const sum = sha(bytes);
    const s = await api.startUpload({ sha256: sum, size: BigInt(bytes.length), use: use() });
    expect(s.alreadyStored).toBe(false);
    const n = Number(s.chunkSize);
    await api.uploadChunk({ uploadId: s.uploadId, index: 0, data: bytes.subarray(0, n) });

    // A restart: the same file again resumes the same upload.
    const again = await api.startUpload({ sha256: sum, size: BigInt(bytes.length), use: use() });
    expect(again.uploadId).toBe(s.uploadId);
    expect(again.receivedChunks).toEqual([0]);

    await expect(api.uploadChunk({ uploadId: s.uploadId, index: 1, data: bytes.subarray(0, 10) })).rejects.toThrow(/must be/);
    await expect(api.finishUpload({ uploadId: s.uploadId, use: use() })).rejects.toThrow(/not received/);
    await api.uploadChunk({ uploadId: s.uploadId, index: 1, data: bytes.subarray(n, 2 * n) });
    await api.uploadChunk({ uploadId: s.uploadId, index: 2, data: bytes.subarray(2 * n) });
    const done = await api.finishUpload({ uploadId: s.uploadId, use: use() });
    expect(Number(done.record!.ref!.size)).toBe(bytes.length);

    const res = await get(sum, token, { range: `bytes=${n - 2}-${n + 1}` });
    expect([...new Uint8Array(await res.arrayBuffer())]).toEqual([...bytes.subarray(n - 2, n + 2)]);

    // A chunk with the wrong content is found at the end.
    const fake = new Uint8Array(1024).fill(7);
    const bad = await api.startUpload({ sha256: sha("something else"), size: 1024n, use: use() });
    await api.uploadChunk({ uploadId: bad.uploadId, index: 0, data: fake });
    await expect(api.finishUpload({ uploadId: bad.uploadId, use: use() })).rejects.toThrow(/do not match/);
    expect((await get(sha("something else"), token)).status).toBe(404);
  });

  it("re-uploads cost nothing, and objects are per owner", async () => {
    const bytes = new TextEncoder().encode("dedupe me");
    const first = await upload(token, bytes, use({ sessionId: "ses_b" }));
    expect(first.deduped).toBe(false);
    const before = await api.getObjectLimits({});
    const second = await upload(token, bytes, use({ sessionId: "ses_c", name: "copy.bin" }));
    expect(second.deduped).toBe(true);
    expect((await api.getObjectLimits({})).usedBytes).toBe(before.usedBytes);
    expect((await api.listSessionObjects({ sessionId: "ses_c" })).records.map((r) => r.ref!.name)).toEqual(["copy.bin"]);

    // Someone else knows the hash, not the bytes.
    expect((await get(first.sum, other)).status).toBe(404);
    await expect(objects(other).statObject({ sha256: first.sum })).rejects.toThrow(/not found/i);
    expect((await fetch(`${srv.url}/api/objects/${first.sum}`)).status).toBe(401);
    // And has to send them to get a copy of their own.
    expect((await upload(other, bytes, use())).deduped).toBe(false);
    expect((await get(first.sum, other)).status).toBe(200);
  });

  it("never renders user HTML or SVG", async () => {
    for (const [type, text] of [
      ["text/html", "<script>alert(1)</script>"],
      ["image/svg+xml", "<svg onload='alert(1)'/>"],
    ] as const) {
      const { sum } = await upload(token, new TextEncoder().encode(text), use({ contentType: type, name: "x" }));
      const res = await get(sum, token);
      expect(res.headers.get("content-type")).toBe("application/octet-stream");
      expect(res.headers.get("content-disposition")).toMatch(/^attachment/);
      expect(res.headers.get("content-security-policy")).toMatch(/^sandbox/);
    }
  });

  it("enforces the file limit and the per-user quota", async () => {
    process.env.HARNESS_OBJECTS_MAX_FILE_BYTES = "100";
    await expect(api.startUpload({ sha256: sha("big"), size: 101n, use: use() })).rejects.toThrow(/limit/);
    delete process.env.HARNESS_OBJECTS_MAX_FILE_BYTES;

    const o = objects(other);
    const { used, id } = await (async () => {
      const l = await o.getObjectLimits({});
      const me = await srv.as(other).auth.getMe({});
      return { used: Number(l.usedBytes), id: me.user!.id };
    })();
    const admin = await adminApi(srv.url, token, "PATCH", "/api/admin/quotas", { users: { [id]: { objects_bytes: used + 10 } } });
    expect(admin.status).toBe(200);
    expect(Number((await o.getObjectLimits({})).quotaBytes)).toBe(used + 10);
    await expect(o.startUpload({ sha256: sha("over"), size: 11n, use: use() })).rejects.toThrow(/quota/i);
    // What is already stored does not count again.
    const same = new TextEncoder().encode("dedupe me");
    expect((await upload(other, same, use({ sessionId: "ses_z" }))).deduped).toBe(true);
    expect((await upload(other, new Uint8Array(10).fill(3))).deduped).toBe(false);
  });

  it("deleting a session deletes its objects unless still referenced, and the sweep removes orphans", async () => {
    const onlyHere = new TextEncoder().encode("only in the deleted session");
    const shared = new TextEncoder().encode("in two sessions");
    const sessionId = "ses_del";
    const { blobStore } = await import("../src/services/blobstore.ts");
    const a = await upload(token, onlyHere, use({ sessionId }));
    const b = await upload(token, shared, use({ sessionId }));
    await upload(token, shared, use({ sessionId: "ses_keep" }));

    const { deleteSessionObjects, sweepObjects } = await import("../src/services/objects.ts");
    const me = await srv.as(token).auth.getMe({});
    await deleteSessionObjects(me.user!.id, sessionId);
    expect((await get(a.sum, token)).status).toBe(404);
    expect((await get(b.sum, token)).status).toBe(200);
    expect(await blobStore().get(`u/${me.user!.id}/${a.sum}`)).toBeUndefined();

    // Bytes nothing refers to, younger than the grace, stay; later they go.
    const key = `u/${me.user!.id}/${"f".repeat(64)}`;
    await blobStore().put(key, new Uint8Array([1, 2, 3]));
    await sweepObjects();
    expect(await blobStore().get(key)).toBeDefined();
    const later = new Date(Date.now() + 40 * 24 * 3600_000);
    const abandoned = await api.startUpload({ sha256: sha("abandoned"), size: 10n, use: use() });
    const report = await sweepObjects(later);
    expect(report.blobs).toBeGreaterThanOrEqual(1);
    // ses_keep has no session row: past the grace its records go too.
    expect(report.records).toBeGreaterThanOrEqual(1);
    expect(report.uploads).toBeGreaterThanOrEqual(1);
    expect(await blobStore().get(key)).toBeUndefined();
    await expect(api.uploadChunk({ uploadId: abandoned.uploadId, index: 0, data: new Uint8Array(10) })).rejects.toThrow(/not found/i);
  });

  it("deletes an object explicitly", async () => {
    const { sum } = await upload(token, new TextEncoder().encode("delete me"));
    // Only the use asked for goes while another one remains.
    await upload(token, new TextEncoder().encode("delete me"), use({ name: "second.bin" }));
    await api.deleteObject({ sha256: sum, sessionId: "ses_a", kind: ObjectKind.SCRATCH, name: "second.bin" });
    expect((await get(sum, token)).status).toBe(200);
    await api.deleteObject({ sha256: sum });
    expect((await get(sum, token)).status).toBe(404);
    await expect(api.deleteObject({ sha256: sum })).rejects.toThrow(/not found/i);
  });
});
