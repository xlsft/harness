/**
 * Where object bytes live, behind one small interface (src/services/objects.ts decides everything else):
 *   - S3Store: any S3 API. In the image that is the bundled Garage on 127.0.0.1:3900 (deploy/README.md), or an
 *     external S3 through HARNESS_S3_ENDPOINT. Signed by hand with SigV4 over fetch; nothing here needs an SDK.
 *   - FsStore: files under $HARNESS_DATA_DIR/objects, for development and tests where no S3 binary is at hand.
 * Clients never reach either: every read and write goes through core.
 */
import { createHash, createHmac, randomBytes } from "node:crypto";
import { createReadStream, existsSync, readFileSync } from "node:fs";
import { mkdir, open, readdir, rename, rm, stat, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { Readable } from "node:stream";
import { config } from "../env.ts";
import { ApiError } from "../lib/errors.ts";

export interface BlobRange {
  start: number;
  /** Inclusive. */
  end: number;
}

export interface BlobPart {
  partNumber: number;
  etag: string;
}

export interface BlobItem {
  key: string;
  lastModified: Date;
}

/** Keys are "/"-separated, without empty or dot segments. Parts number from 1; all but the last are at least 5 MiB (S3). */
export interface BlobStore {
  readonly kind: "s3" | "fs";
  put(key: string, data: Uint8Array): Promise<void>;
  startMultipart(key: string): Promise<string>;
  putPart(key: string, uploadId: string, partNumber: number, data: Uint8Array): Promise<string>;
  completeMultipart(key: string, uploadId: string, parts: BlobPart[]): Promise<void>;
  abortMultipart(key: string, uploadId: string): Promise<void>;
  /** The bytes (of `range`), or undefined when the key does not exist. */
  get(key: string, range?: BlobRange): Promise<ReadableStream<Uint8Array> | undefined>;
  /** A missing key is not an error. */
  delete(key: string): Promise<void>;
  list(prefix: string, after?: string): Promise<{ items: BlobItem[]; next?: string }>;
}

function storeError(what: string, detail: string): ApiError {
  console.error(`[harness] object store: ${what}: ${detail}`);
  return new ApiError("overloaded", "The object store is unavailable", { status: 503, retryAfterMs: 5000 });
}

// --- S3 ---------------------------------------------------------------------------------------------------

export interface S3Options {
  endpoint: string;
  bucket: string;
  region: string;
  accessKey: string;
  secretKey: string;
}

const EMPTY_SHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

const sha256Hex = (data: string | Uint8Array) => createHash("sha256").update(data).digest("hex");
const hmac = (key: string | Buffer, data: string) => createHmac("sha256", key).update(data).digest();

/** RFC 3986 percent-encoding as SigV4 wants it: everything but unreserved characters. */
function uriEncode(s: string): string {
  return encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
}

/** The Authorization header of AWS Signature V4. `headers` are the signed ones, names in lowercase, host included. */
export function sigV4Authorization(input: {
  method: string;
  path: string;
  query: string;
  headers: Record<string, string>;
  payloadHash: string;
  amzDate: string;
  region: string;
  accessKey: string;
  secretKey: string;
}): string {
  const names = Object.keys(input.headers).sort();
  const canonicalHeaders = names.map((n) => `${n}:${input.headers[n]!.trim()}\n`).join("");
  const signedHeaders = names.join(";");
  const request = [input.method, input.path, input.query, canonicalHeaders, signedHeaders, input.payloadHash].join("\n");
  const day = input.amzDate.slice(0, 8);
  const scope = `${day}/${input.region}/s3/aws4_request`;
  const toSign = ["AWS4-HMAC-SHA256", input.amzDate, scope, sha256Hex(request)].join("\n");
  const signingKey = hmac(hmac(hmac(hmac(`AWS4${input.secretKey}`, day), input.region), "s3"), "aws4_request");
  const signature = createHmac("sha256", signingKey).update(toSign).digest("hex");
  return `AWS4-HMAC-SHA256 Credential=${input.accessKey}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`;
}

function tagValue(xml: string, tag: string): string | undefined {
  const m = new RegExp(`<${tag}>([^<]*)</${tag}>`).exec(xml);
  return m ? unescapeXml(m[1]!) : undefined;
}

function unescapeXml(s: string): string {
  return s.replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}

function escapeXml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

export class S3Store implements BlobStore {
  readonly kind = "s3" as const;
  private readonly base: URL;

  constructor(private readonly o: S3Options) {
    this.base = new URL(o.endpoint.replace(/\/+$/, "") + "/");
  }

  /** Path-style addressing (bucket in the path): every S3 implementation takes it and it needs no wildcard DNS. */
  private async call(
    method: string,
    key: string,
    params: Record<string, string> = {},
    body?: Uint8Array | string,
    extra: Record<string, string> = {},
  ): Promise<Response> {
    const basePath = this.base.pathname.replace(/\/$/, "");
    const path = `${basePath}/${uriEncode(this.o.bucket)}${key ? "/" + key.split("/").map(uriEncode).join("/") : ""}`;
    const query = Object.keys(params)
      .sort()
      .map((k) => `${uriEncode(k)}=${uriEncode(params[k]!)}`)
      .join("&");
    const amzDate = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d{3}/, "");
    const payloadHash = body === undefined ? EMPTY_SHA256 : sha256Hex(body);
    const signed: Record<string, string> = { host: this.base.host, "x-amz-content-sha256": payloadHash, "x-amz-date": amzDate };
    for (const [k, v] of Object.entries(extra)) signed[k.toLowerCase()] = v;
    const authorization = sigV4Authorization({
      method,
      path,
      query,
      headers: signed,
      payloadHash,
      amzDate,
      region: this.o.region,
      accessKey: this.o.accessKey,
      secretKey: this.o.secretKey,
    });
    const { host: _host, ...headers } = signed;
    const url = `${this.base.origin}${path}${query ? `?${query}` : ""}`;
    try {
      return await fetch(url, { method, headers: { ...headers, authorization }, body: body as BodyInit | undefined });
    } catch (err) {
      throw storeError(`${method} ${key}`, err instanceof Error ? err.message : String(err));
    }
  }

  private async check(what: string, res: Response): Promise<string> {
    const text = await res.text();
    if (!res.ok) throw storeError(what, `${res.status} ${tagValue(text, "Code") ?? ""} ${tagValue(text, "Message") ?? ""}`);
    return text;
  }

  async put(key: string, data: Uint8Array): Promise<void> {
    await this.check("put", await this.call("PUT", key, {}, data));
  }

  async startMultipart(key: string): Promise<string> {
    const xml = await this.check("start multipart", await this.call("POST", key, { uploads: "" }));
    const id = tagValue(xml, "UploadId");
    if (!id) throw storeError("start multipart", "no UploadId in the answer");
    return id;
  }

  async putPart(key: string, uploadId: string, partNumber: number, data: Uint8Array): Promise<string> {
    const res = await this.call("PUT", key, { partNumber: String(partNumber), uploadId }, data);
    await this.check("put part", res);
    const etag = res.headers.get("etag");
    if (!etag) throw storeError("put part", "no ETag in the answer");
    return etag;
  }

  async completeMultipart(key: string, uploadId: string, parts: BlobPart[]): Promise<void> {
    const body =
      "<CompleteMultipartUpload>" +
      parts.map((p) => `<Part><PartNumber>${p.partNumber}</PartNumber><ETag>${escapeXml(p.etag)}</ETag></Part>`).join("") +
      "</CompleteMultipartUpload>";
    const xml = await this.check("complete multipart", await this.call("POST", key, { uploadId }, body));
    // S3 can answer 200 and fail in the body.
    if (xml.includes("<Error>")) throw storeError("complete multipart", tagValue(xml, "Message") ?? xml);
  }

  async abortMultipart(key: string, uploadId: string): Promise<void> {
    const res = await this.call("DELETE", key, { uploadId });
    if (res.status === 404) {
      await res.body?.cancel();
      return;
    }
    await this.check("abort multipart", res);
  }

  async get(key: string, range?: BlobRange): Promise<ReadableStream<Uint8Array> | undefined> {
    const res = await this.call("GET", key, {}, undefined, range ? { range: `bytes=${range.start}-${range.end}` } : {});
    if (res.status === 404) {
      await res.body?.cancel();
      return undefined;
    }
    if (!res.ok || !res.body) throw storeError("get", `${res.status} ${await res.text()}`);
    return res.body as ReadableStream<Uint8Array>;
  }

  async delete(key: string): Promise<void> {
    const res = await this.call("DELETE", key);
    if (res.status === 404) {
      await res.body?.cancel();
      return;
    }
    await this.check("delete", res);
  }

  async list(prefix: string, after?: string): Promise<{ items: BlobItem[]; next?: string }> {
    const params: Record<string, string> = { "list-type": "2", prefix, "max-keys": "500" };
    if (after) params["continuation-token"] = after;
    const xml = await this.check("list", await this.call("GET", "", params));
    const items: BlobItem[] = [];
    for (const m of xml.matchAll(/<Contents>([\s\S]*?)<\/Contents>/g)) {
      const key = tagValue(m[1]!, "Key");
      const at = tagValue(m[1]!, "LastModified");
      if (key) items.push({ key, lastModified: at ? new Date(at) : new Date() });
    }
    return { items, next: tagValue(xml, "IsTruncated") === "true" ? tagValue(xml, "NextContinuationToken") : undefined };
  }
}

// --- files ------------------------------------------------------------------------------------------------

/** The S3 calls over a directory: <root>/blobs/<key> and <root>/uploads/<id>/<partNumber>. */
export class FsStore implements BlobStore {
  readonly kind = "fs" as const;

  constructor(private readonly root: string) {}

  private blob(key: string): string {
    const segments = key.split("/");
    if (!segments.length || segments.some((s) => !s || s === "." || s === "..")) throw new ApiError("invalid_request", "Invalid object key");
    return join(this.root, "blobs", ...segments.map(encodeURIComponent));
  }

  private upload(id: string): string {
    if (!/^[A-Za-z0-9_-]+$/.test(id)) throw new ApiError("invalid_request", "Invalid upload id");
    return join(this.root, "uploads", id);
  }

  async put(key: string, data: Uint8Array): Promise<void> {
    const path = this.blob(key);
    await mkdir(dirname(path), { recursive: true });
    const tmp = `${path}.${randomBytes(6).toString("hex")}.tmp`;
    await writeFile(tmp, data);
    await rename(tmp, path);
  }

  async startMultipart(_key: string): Promise<string> {
    const id = randomBytes(12).toString("hex");
    await mkdir(this.upload(id), { recursive: true });
    return id;
  }

  async putPart(_key: string, uploadId: string, partNumber: number, data: Uint8Array): Promise<string> {
    const dir = this.upload(uploadId);
    if (!existsSync(dir)) throw new ApiError("not_found", "No such upload");
    const tmp = join(dir, `${partNumber}.${randomBytes(6).toString("hex")}.tmp`);
    await writeFile(tmp, data);
    await rename(tmp, join(dir, String(partNumber)));
    return `"${sha256Hex(data)}"`;
  }

  async completeMultipart(key: string, uploadId: string, parts: BlobPart[]): Promise<void> {
    const dir = this.upload(uploadId);
    const path = this.blob(key);
    await mkdir(dirname(path), { recursive: true });
    const tmp = `${path}.${randomBytes(6).toString("hex")}.tmp`;
    const out = await open(tmp, "w");
    try {
      for (const p of [...parts].sort((a, b) => a.partNumber - b.partNumber)) {
        for await (const chunk of createReadStream(join(dir, String(p.partNumber)))) await out.write(chunk as Buffer);
      }
    } finally {
      await out.close();
    }
    await rename(tmp, path);
    await rm(dir, { recursive: true, force: true });
  }

  async abortMultipart(_key: string, uploadId: string): Promise<void> {
    await rm(this.upload(uploadId), { recursive: true, force: true });
  }

  async get(key: string, range?: BlobRange): Promise<ReadableStream<Uint8Array> | undefined> {
    const path = this.blob(key);
    if (!existsSync(path)) return undefined;
    const stream = createReadStream(path, range ? { start: range.start, end: range.end } : {});
    return Readable.toWeb(stream) as ReadableStream<Uint8Array>;
  }

  async delete(key: string): Promise<void> {
    await rm(this.blob(key), { force: true });
  }

  async list(prefix: string, _after?: string): Promise<{ items: BlobItem[]; next?: string }> {
    const items: BlobItem[] = [];
    const base = join(this.root, "blobs");
    const walk = async (dir: string, parts: string[]): Promise<void> => {
      let entries;
      try {
        entries = await readdir(dir, { withFileTypes: true });
      } catch {
        return;
      }
      for (const e of entries) {
        const full = join(dir, e.name);
        const key = [...parts, decodeURIComponent(e.name)];
        if (e.isDirectory()) await walk(full, key);
        else if (e.isFile() && !e.name.endsWith(".tmp") && key.join("/").startsWith(prefix)) {
          items.push({ key: key.join("/"), lastModified: (await stat(full)).mtime });
        }
      }
    };
    await walk(base, []);
    return { items };
  }
}

// --- configuration ----------------------------------------------------------------------------------------

/** S3 settings from the environment; undefined when no endpoint is set. Keys come from the env or from a file. */
export function s3OptionsFromEnv(env: NodeJS.ProcessEnv = process.env): S3Options | undefined {
  const endpoint = env.HARNESS_S3_ENDPOINT;
  if (!endpoint) return undefined;
  let accessKey = env.HARNESS_S3_ACCESS_KEY ?? "";
  let secretKey = env.HARNESS_S3_SECRET_KEY ?? "";
  // The image's own Garage: init-data generates the keys into this file on first start (never in the environment).
  if ((!accessKey || !secretKey) && env.HARNESS_S3_CREDENTIALS_FILE) {
    const [id, secret] = readFileSync(resolve(env.HARNESS_S3_CREDENTIALS_FILE), "utf8").trim().split(/\s+/);
    accessKey = id ?? "";
    secretKey = secret ?? "";
  }
  if (!accessKey || !secretKey) {
    throw new Error("HARNESS_S3_ENDPOINT needs HARNESS_S3_ACCESS_KEY and HARNESS_S3_SECRET_KEY (or HARNESS_S3_CREDENTIALS_FILE)");
  }
  return {
    endpoint,
    bucket: env.HARNESS_S3_BUCKET || "harness-objects",
    region: env.HARNESS_S3_REGION || "garage",
    accessKey,
    secretKey,
  };
}

let store: BlobStore | undefined;

export function blobStore(): BlobStore {
  if (store) return store;
  const s3 = s3OptionsFromEnv();
  store = s3 ? new S3Store(s3) : new FsStore(join(config().dataDir, "objects"));
  return store;
}

/** For tests. */
export function resetBlobStore(next?: BlobStore): void {
  store = next;
}
