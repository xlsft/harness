/**
 * The binary AWS event stream Bedrock's InvokeModelWithResponseStream answers with, turned into the Anthropic SSE text its
 * chunks carry: each frame's payload is `{"bytes": "<base64 JSON event>"}`, so the Anthropic adapter reads the result as usual.
 *
 * Frame: total length (u32), headers length (u32), prelude CRC (u32), headers, payload, message CRC (u32). A frame with a bad
 * checksum ends the stream with an error: a damaged response must not pass for a short one.
 */
import { crc32 } from "node:zlib";

export interface EventFrame {
  headers: Record<string, string>;
  payload: Uint8Array;
}

const PRELUDE = 12;
const TRAILER = 4;

function parseHeaders(b: Uint8Array): Record<string, string> {
  const out: Record<string, string> = {};
  const view = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const text = new TextDecoder();
  let i = 0;
  while (i < b.length) {
    const nameLen = b[i]!;
    const name = text.decode(b.subarray(i + 1, i + 1 + nameLen));
    i += 1 + nameLen;
    const type = b[i]!;
    i += 1;
    switch (type) {
      case 0: // true
      case 1: // false
        break;
      case 2:
        i += 1;
        break;
      case 3:
        i += 2;
        break;
      case 4:
        i += 4;
        break;
      case 5:
      case 8:
        i += 8;
        break;
      case 6:
      case 7: {
        const len = view.getUint16(i);
        if (type === 7) out[name] = text.decode(b.subarray(i + 2, i + 2 + len));
        i += 2 + len;
        break;
      }
      case 9:
        i += 16;
        break;
      default:
        throw new Error(`unknown event stream header type ${type}`);
    }
  }
  return out;
}

/** Splits a byte stream into frames; throws on a damaged frame. */
export async function* readFrames(body: ReadableStream<Uint8Array>): AsyncGenerator<EventFrame> {
  let buf: Uint8Array = new Uint8Array(0);
  for await (const chunk of body as unknown as AsyncIterable<Uint8Array>) {
    const next = new Uint8Array(buf.length + chunk.length);
    next.set(buf);
    next.set(chunk, buf.length);
    buf = next;
    while (buf.length >= PRELUDE) {
      const view = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
      const total = view.getUint32(0);
      const headersLen = view.getUint32(4);
      if (crc32(buf.subarray(0, 8)) !== view.getUint32(8)) throw new Error("event stream prelude checksum mismatch");
      if (total < PRELUDE + TRAILER + headersLen) throw new Error("event stream frame length is invalid");
      if (buf.length < total) break;
      if (crc32(buf.subarray(0, total - TRAILER)) !== view.getUint32(total - TRAILER)) throw new Error("event stream message checksum mismatch");
      yield {
        headers: parseHeaders(buf.subarray(PRELUDE, PRELUDE + headersLen)),
        payload: buf.slice(PRELUDE + headersLen, total - TRAILER),
      };
      buf = buf.subarray(total);
    }
  }
  if (buf.length > 0) throw new Error("event stream ended inside a frame");
}

/** Encodes one frame with string headers. Used by the tests' fake upstream. */
export function encodeFrame(headers: Record<string, string>, payload: Uint8Array | string): Uint8Array {
  const enc = new TextEncoder();
  const body = typeof payload === "string" ? enc.encode(payload) : payload;
  const parts: number[] = [];
  for (const [k, v] of Object.entries(headers)) {
    const name = enc.encode(k);
    const value = enc.encode(v);
    parts.push(name.length, ...name, 7, value.length >> 8, value.length & 0xff, ...value);
  }
  const total = PRELUDE + parts.length + body.length + TRAILER;
  const out = new Uint8Array(total);
  const view = new DataView(out.buffer);
  view.setUint32(0, total);
  view.setUint32(4, parts.length);
  view.setUint32(8, crc32(out.subarray(0, 8)));
  out.set(parts, PRELUDE);
  out.set(body, PRELUDE + parts.length);
  view.setUint32(total - TRAILER, crc32(out.subarray(0, total - TRAILER)));
  return out;
}

/** Bedrock's exception names, as the Anthropic error types the shared stream reader classifies. */
function errorType(name: string): string {
  if (/throttl/i.test(name)) return "rate_limit_error";
  if (/overload|unavailable|modelnotready/i.test(name)) return "overloaded_error";
  if (/validation/i.test(name)) return "invalid_request_error";
  if (/accessdenied/i.test(name)) return "permission_error";
  return "api_error";
}

const sse = (data: unknown) => `data: ${JSON.stringify(data)}\n\n`;

/** The Anthropic SSE text carried by a Bedrock event stream. */
export function eventStreamToSse(body: ReadableStream<Uint8Array>): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  const text = new TextDecoder();
  const frames = readFrames(body);
  return new ReadableStream<Uint8Array>({
    async pull(ctl) {
      try {
        const { value, done } = await frames.next();
        if (done) return ctl.close();
        const kind = value.headers[":message-type"];
        const raw = text.decode(value.payload);
        if (kind === "exception" || kind === "error") {
          const name = value.headers[":exception-type"] ?? value.headers[":error-code"] ?? "error";
          let message = value.headers[":error-message"] ?? raw;
          try {
            message = (JSON.parse(raw) as { message?: string }).message ?? message;
          } catch {
            // Not JSON: keep the text.
          }
          ctl.enqueue(enc.encode(sse({ type: "error", error: { type: errorType(name), message: `${name}: ${message}` } })));
          return ctl.close();
        }
        const parsed = JSON.parse(raw) as { bytes?: string };
        if (parsed.bytes) ctl.enqueue(enc.encode(`data: ${Buffer.from(parsed.bytes, "base64").toString("utf8")}\n\n`));
      } catch (err) {
        ctl.enqueue(enc.encode(sse({ type: "error", error: { type: "api_error", message: (err as Error).message } })));
        ctl.close();
      }
    },
    async cancel() {
      await frames.return(undefined);
    },
  });
}
