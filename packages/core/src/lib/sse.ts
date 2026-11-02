/** Minimal, spec-compliant Server-Sent Events parser (for upstream provider streams). */

export interface SseMessage {
  event: string;
  data: string;
  id?: string;
}

/** Parses an SSE byte stream. Handles CRLF/LF/CR, multi-line data, comments and a trailing message without blank line. */
export async function* parseSse(body: AsyncIterable<Uint8Array> | ReadableStream<Uint8Array>): AsyncGenerator<SseMessage> {
  const decoder = new TextDecoder();
  let buf = "";
  let event = "";
  let data: string[] = [];
  let id: string | undefined;
  let sawField = false;

  const flush = (): SseMessage | undefined => {
    if (!sawField) return undefined;
    const msg: SseMessage = { event: event || "message", data: data.join("\n") };
    if (id !== undefined) msg.id = id;
    event = "";
    data = [];
    sawField = false;
    return msg;
  };

  const handleLine = (line: string): SseMessage | undefined => {
    if (line === "") return flush();
    if (line.startsWith(":")) return undefined;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") {
      event = value;
      sawField = true;
    } else if (field === "data") {
      data.push(value);
      sawField = true;
    } else if (field === "id") {
      id = value;
    }
    return undefined;
  };

  for await (const chunk of body as AsyncIterable<Uint8Array>) {
    buf += decoder.decode(chunk, { stream: true });
    let start = 0;
    for (let i = 0; i < buf.length; i++) {
      const ch = buf[i];
      if (ch !== "\n" && ch !== "\r") continue;
      // A CR at the very end may be the first half of CRLF: wait for more data.
      if (ch === "\r" && i === buf.length - 1) break;
      const line = buf.slice(start, i);
      if (ch === "\r" && buf[i + 1] === "\n") i++;
      start = i + 1;
      const msg = handleLine(line);
      if (msg) yield msg;
    }
    buf = buf.slice(start);
  }
  buf += decoder.decode();
  if (buf.length) {
    const msg = handleLine(buf.replace(/\r$/, ""));
    if (msg) yield msg;
  }
  const last = flush();
  if (last) yield last;
}
