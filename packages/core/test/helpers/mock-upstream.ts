/** Scriptable local HTTP server standing in for Anthropic / OpenAI-compatible upstreams. */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export interface RecordedRequest {
  method: string;
  url: string;
  headers: IncomingMessage["headers"];
  body: any;
}

export interface MockResponse {
  status?: number;
  headers?: Record<string, string>;
  /** JSON body (non-streaming). */
  json?: unknown;
  /** Raw SSE text; split into `chunks` pieces to exercise the parser. */
  sse?: string;
  chunkSize?: number;
  /** Delay between chunks. */
  delayMs?: number;
  /** Stop sending after this many characters of `sse` and keep the connection open (a stalled upstream). */
  stallAfter?: number;
}

export type MockHandler = (req: RecordedRequest) => MockResponse;

export class MockUpstream {
  readonly requests: RecordedRequest[] = [];
  private queue: MockHandler[] = [];
  fallback: MockHandler = () => ({ status: 500, json: { error: { type: "api_error", message: "no mock response queued" } } });
  private server?: Server;
  url = "";

  enqueue(...handlers: (MockHandler | MockResponse)[]): this {
    for (const h of handlers) this.queue.push(typeof h === "function" ? h : () => h);
    return this;
  }

  reset(): void {
    this.requests.length = 0;
    this.queue = [];
  }

  async start(): Promise<this> {
    this.server = createServer((req, res) => void this.handle(req, res));
    await new Promise<void>((r) => this.server!.listen(0, "127.0.0.1", r));
    this.url = `http://127.0.0.1:${(this.server!.address() as AddressInfo).port}`;
    return this;
  }

  async stop(): Promise<void> {
    this.server?.closeAllConnections();
    await new Promise<void>((r) => this.server?.close(() => r()) ?? r());
  }

  private async handle(req: IncomingMessage, res: ServerResponse) {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    const text = Buffer.concat(chunks).toString("utf8");
    let body: any = text;
    try {
      body = text ? JSON.parse(text) : undefined;
    } catch {
      // keep raw
    }
    const rec: RecordedRequest = { method: req.method ?? "", url: req.url ?? "", headers: req.headers, body };
    this.requests.push(rec);
    const handler = this.queue.shift() ?? this.fallback;
    const r = handler(rec);
    const status = r.status ?? 200;
    if (r.sse !== undefined) {
      res.writeHead(status, { "content-type": "text/event-stream", ...r.headers });
      const size = r.chunkSize ?? 37;
      const text = r.stallAfter === undefined ? r.sse : r.sse.slice(0, r.stallAfter);
      for (let i = 0; i < text.length; i += size) {
        res.write(text.slice(i, Math.min(i + size, text.length)));
        if (r.delayMs) await new Promise((x) => setTimeout(x, r.delayMs));
      }
      if (r.stallAfter !== undefined) return;
      res.end();
      return;
    }
    res.writeHead(status, { "content-type": "application/json", ...r.headers });
    res.end(JSON.stringify(r.json ?? {}));
  }
}

/** Builds an SSE string from [event, data] pairs (event null = data-only, as OpenAI does). */
export function sse(events: [string | null, unknown][]): string {
  return events
    .map(([ev, data]) => `${ev ? `event: ${ev}\n` : ""}data: ${typeof data === "string" ? data : JSON.stringify(data)}\n\n`)
    .join("");
}
