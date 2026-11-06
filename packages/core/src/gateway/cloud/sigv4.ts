/**
 * AWS Signature Version 4 for one HTTP request. Written from the published algorithm; the unit tests run it against
 * AWS's own signature test vectors.
 */
import { createHash, createHmac } from "node:crypto";

export interface AwsCredentials {
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken?: string;
  /** Epoch ms; absent for long-lived keys. */
  expiresAt?: number;
}

export interface SignInput {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
  region: string;
  service: string;
  credentials: AwsCredentials;
  now?: Date;
}

const sha256 = (data: string) => createHash("sha256").update(data, "utf8").digest("hex");
const hmac = (key: Buffer | string, data: string) => createHmac("sha256", key).update(data, "utf8").digest();

/** RFC 3986 encoding as SigV4 wants it: everything but unreserved characters. */
function encode(s: string): string {
  return encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
}

function canonicalQuery(params: URLSearchParams): string {
  return [...params.entries()]
    .map(([k, v]) => [encode(k), encode(v)] as const)
    .sort((a, b) => (a[0] === b[0] ? (a[1] < b[1] ? -1 : 1) : a[0] < b[0] ? -1 : 1))
    .map(([k, v]) => `${k}=${v}`)
    .join("&");
}

/** Headers that take part in the signature: host, content-type and every x-amz-*. Others may change on the way. */
function signable(name: string): boolean {
  return name === "host" || name === "content-type" || name.startsWith("x-amz-");
}

/** Returns the request's headers with x-amz-date, the session token if any, and Authorization added. */
export function signV4(input: SignInput): Record<string, string> {
  const url = new URL(input.url);
  const now = input.now ?? new Date();
  const amzDate = now
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d+Z$/, "Z");
  const day = amzDate.slice(0, 8);

  const out: Record<string, string> = { ...input.headers, "x-amz-date": amzDate };
  if (input.credentials.sessionToken) out["x-amz-security-token"] = input.credentials.sessionToken;

  const lowered = new Map<string, string>([["host", url.host]]);
  for (const [k, v] of Object.entries(out)) {
    const name = k.toLowerCase();
    if (signable(name)) lowered.set(name, v.trim().replace(/\s+/g, " "));
  }
  const names = [...lowered.keys()].sort();
  const canonicalHeaders = names.map((n) => `${n}:${lowered.get(n)}\n`).join("");
  const signedHeaders = names.join(";");

  // Every service but S3 signs the path as sent, each segment encoded once more.
  const path = url.pathname.split("/").map(encode).join("/");
  const canonical = [input.method.toUpperCase(), path, canonicalQuery(url.searchParams), canonicalHeaders, signedHeaders, sha256(input.body)].join("\n");

  const scope = `${day}/${input.region}/${input.service}/aws4_request`;
  const toSign = ["AWS4-HMAC-SHA256", amzDate, scope, sha256(canonical)].join("\n");
  const key = hmac(hmac(hmac(hmac(`AWS4${input.credentials.secretAccessKey}`, day), input.region), input.service), "aws4_request");
  const signature = hmac(key, toSign).toString("hex");
  out.authorization = `AWS4-HMAC-SHA256 Credential=${input.credentials.accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`;
  return out;
}
