/**
 * Export of what the server does, to local fakes: OTLP metrics and traces to a fake collector, audit events to a webhook and
 * to syslog over UDP and TCP, with delivery surviving a collector that was down.
 */
import { createHmac } from "node:crypto";
import { createSocket, type Socket } from "node:dgram";
import { createServer, type Server } from "node:http";
import { createServer as createTcpServer, type AddressInfo, type Server as TcpServer } from "node:net";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { flushForwarders, syslogMessage } from "../src/audit/forward.ts";
import { record, type AuditRow } from "../src/audit/log.ts";
import { countMetric, recordMetric } from "../src/telemetry/metrics.ts";
import { flushTelemetry, startTelemetry, stopTelemetry } from "../src/telemetry/otel.ts";
import { deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

interface Seen {
  path: string;
  headers: Record<string, string | string[] | undefined>;
  body: Buffer;
}

function collector(): Promise<{ url: string; seen: Seen[]; status: { code: number }; server: Server }> {
  const seen: Seen[] = [];
  const status = { code: 200 };
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    seen.push({ path: req.url!, headers: req.headers, body: Buffer.concat(chunks) });
    res.writeHead(status.code, { "content-type": "application/json" });
    res.end("{}");
  });
  return new Promise((r) => server.listen(0, "127.0.0.1", () => r({ url: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, seen, status, server })));
}

const close = (s: { closeAllConnections?: () => void; close: (cb: () => void) => void }) =>
  new Promise<void>((r) => {
    s.closeAllConnections?.();
    s.close(() => r());
  });

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;

beforeAll(async () => {
  ts = await startTestServer();
  admin = ts.as((await deviceLogin(ts)).accessToken);
});

afterAll(async () => {
  await ts.close();
});

describe("OpenTelemetry", () => {
  it("stays off without configuration", () => {
    expect(startTelemetry({})).toBe(false);
    expect(startTelemetry({ OTEL_EXPORTER_OTLP_ENDPOINT: "http://x", OTEL_SDK_DISABLED: "true" })).toBe(false);
  });

  it("exports metrics and traces of real requests over OTLP, by environment", async () => {
    const c = await collector();
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = c.url;
    process.env.OTEL_EXPORTER_OTLP_PROTOCOL = "http/json";
    process.env.OTEL_EXPORTER_OTLP_HEADERS = "x-test-key=abc";
    process.env.OTEL_SERVICE_NAME = "harness-under-test";
    try {
      expect(startTelemetry(process.env)).toBe(true);
      await admin.admin.listUsers({});
      countMetric("harness.llm.tokens", 12, { kind: "input", model: "m" });
      recordMetric("harness.llm.duration", 34, { model: "m" }, "ms");
      await flushTelemetry();

      const traces = c.seen.filter((s) => s.path === "/v1/traces");
      const metrics = c.seen.filter((s) => s.path === "/v1/metrics");
      expect(traces.length).toBeGreaterThan(0);
      expect(metrics.length).toBeGreaterThan(0);
      expect(traces[0]!.headers["x-test-key"]).toBe("abc");
      const t = traces.map((s) => s.body.toString()).join("");
      expect(t).toContain("harness.auth.v1.AdminService/ListUsers");
      expect(t).toContain("harness-under-test");
      const m = metrics.map((s) => s.body.toString()).join("");
      expect(m).toContain("harness.rpc.duration");
      expect(m).toContain("harness.llm.tokens");
      // No credentials in anything exported.
      expect(t + m).not.toContain("hat_");
    } finally {
      await stopTelemetry();
      for (const k of ["OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_HEADERS", "OTEL_SERVICE_NAME"]) delete process.env[k];
      await close(c.server);
    }
  });

  it("continues a trace a caller started", async () => {
    const c = await collector();
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = c.url;
    process.env.OTEL_EXPORTER_OTLP_PROTOCOL = "http/json";
    try {
      startTelemetry(process.env);
      const traceId = "0af7651916cd43dd8448eb211c80319c";
      await admin.admin.listUsers({}, { headers: { traceparent: `00-${traceId}-b7ad6b7169203331-01` } });
      await flushTelemetry();
      expect(c.seen.filter((s) => s.path === "/v1/traces").map((s) => s.body.toString()).join("")).toContain(traceId);
    } finally {
      await stopTelemetry();
      delete process.env.OTEL_EXPORTER_OTLP_ENDPOINT;
      delete process.env.OTEL_EXPORTER_OTLP_PROTOCOL;
      await close(c.server);
    }
  });
});

describe("SIEM forwarding", () => {
  beforeEach(() => {
    delete process.env.HARNESS_AUDIT_WEBHOOK_URL;
    delete process.env.HARNESS_AUDIT_WEBHOOK_SECRET;
    delete process.env.HARNESS_AUDIT_SYSLOG_URL;
  });

  it("posts events to a webhook, signed, and retries what a down collector missed", async () => {
    const c = await collector();
    process.env.HARNESS_AUDIT_WEBHOOK_URL = `${c.url}/siem`;
    process.env.HARNESS_AUDIT_WEBHOOK_SECRET = "webhook-test-secret";
    await record({ action: "test.first", actor: { id: "a1", label: "a" }, details: { n: 1, password: "nope" } });
    await flushForwarders();
    expect(c.seen.length).toBeGreaterThan(0);
    const events = c.seen.flatMap((s) => JSON.parse(s.body.toString()) as AuditRow[]);
    const first = events.find((e) => e.action === "test.first")!;
    expect(first.actor_id).toBe("a1");
    expect(first.details).toEqual({ n: 1, password: "[redacted]" });
    const sent = c.seen.find((s) => s.body.toString().includes("test.first"))!;
    expect(sent.headers["x-harness-signature"]).toBe(`sha256=${createHmac("sha256", "webhook-test-secret").update(sent.body).digest("hex")}`);

    c.status.code = 503;
    await record({ action: "test.during_outage" });
    await flushForwarders();
    c.status.code = 200;
    await record({ action: "test.after_outage" });
    await flushForwarders();
    const ok = c.seen.slice(-1)[0]!;
    const delivered = JSON.parse(ok.body.toString()) as AuditRow[];
    expect(delivered.map((e) => e.action)).toEqual(expect.arrayContaining(["test.during_outage", "test.after_outage"]));
    await close(c.server);
  });

  it("formats RFC 5424", () => {
    const line = syslogMessage({
      id: 7,
      at: "2026-11-06T10:00:00.000Z",
      action: "user.create",
      outcome: "failure",
      actor_id: "usr_1",
      actor: 'a "quoted" ]name\\',
      actor_ip: "10.0.0.1",
      target_type: "user",
      target_id: "usr_2",
      target: "",
      details: { x: 1 },
    });
    expect(line).toBe('<132>1 2026-11-06T10:00:00.000Z harness harness - user.create [audit@32473 id="7" action="user.create" outcome="failure" actorId="usr_1" actor="a \\"quoted\\" \\]name\\\\" ip="10.0.0.1" targetType="user" targetId="usr_2"] {"x":1}');
  });

  it("sends syslog over UDP", async () => {
    const socket: Socket = createSocket("udp4");
    const got: string[] = [];
    const received = new Promise<void>((resolve) => {
      socket.on("message", (m) => {
        got.push(m.toString());
        if (m.toString().includes("test.udp")) resolve();
      });
    });
    await new Promise<void>((r) => socket.bind(0, "127.0.0.1", r));
    process.env.HARNESS_AUDIT_SYSLOG_URL = `udp://127.0.0.1:${(socket.address() as AddressInfo).port}`;
    await record({ action: "test.udp" });
    await flushForwarders();
    await received;
    expect(got.join("\n")).toContain("test.udp");
    socket.close();
  });

  it("sends syslog over TCP with octet counting", async () => {
    let data = "";
    const done = new Promise<void>((resolve) => {
      const server: TcpServer = createTcpServer((s) => {
        s.on("data", (d) => (data += d.toString()));
        s.on("end", resolve);
      });
      server.listen(0, "127.0.0.1", () => {
        process.env.HARNESS_AUDIT_SYSLOG_URL = `tcp://127.0.0.1:${(server.address() as AddressInfo).port}`;
        void (async () => {
          await record({ action: "test.tcp" });
          await flushForwarders();
        })();
      });
      after.push(server);
    });
    await done;
    expect(data).toMatch(/^\d+ <\d+>1 /);
    expect(data).toContain("test.tcp");
    for (const s of after.splice(0)) await close(s);
  });
});

const after: TcpServer[] = [];
