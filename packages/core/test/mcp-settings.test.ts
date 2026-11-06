/** What Settings > MCP and the agent need beyond the plain proxy: elicitation, list changes, server info, tool lists, audit. */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { ListChanged_Kind, Transport, type CallToolResponse, type Server } from "../src/gen/harness/mcp/v1/mcp_pb.ts";
import { adminApi, createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";
import { startMcpUpstream, type Upstream } from "./helpers/mcp-upstream.ts";

let ts: TestServer;
let admin: Awaited<ReturnType<typeof deviceLogin>>;
let alice: Awaited<ReturnType<typeof deviceLogin>>;
let bob: Awaited<ReturnType<typeof deviceLogin>>;
let A: TestServer["mcp"];
let B: TestServer["mcp"];
let R: TestServer["mcp"];
let up: Upstream;
let n = 0;

beforeAll(async () => {
  ts = await startTestServer();
  admin = await deviceLogin(ts);
  alice = await createUserAndLogin(ts, admin.cookie, "alice@test.local");
  bob = await createUserAndLogin(ts, admin.cookie, "bob@test.local");
  A = ts.as(alice.accessToken).mcp;
  B = ts.as(bob.accessToken).mcp;
  R = ts.as(admin.accessToken).mcp;
  up = await startMcpUpstream({ extras: true });
});

afterAll(async () => {
  await ts.close();
  await up.close();
});

async function server(extra: Record<string, unknown> = {}, c: TestServer["mcp"] = A): Promise<Server> {
  const r = await c.createServer({
    server: { name: `s${++n}`, scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: `${up.url}/mcp` }, ...extra },
  });
  const s = r.server!;
  const p = await adminApi(ts.url, admin.accessToken, "PATCH", `/api/admin/mcp/servers/${s.id}`, { allow_private_network: true });
  expect(p.status).toBe(200);
  return (await c.getServer({ serverId: s.id })).server!;
}

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  return p.then(
    () => undefined,
    (e) => (e as ConnectError).code,
  );
}

/** Runs a call, answering elicitations with `answer`. */
async function runCall(
  c: TestServer["mcp"],
  serverId: string,
  tool: string,
  args: unknown,
  answer: (id: string) => Promise<unknown>,
  timeoutMs = 0,
): Promise<CallToolResponse[]> {
  const out: CallToolResponse[] = [];
  for await (const ev of c.callTool({ serverId, toolName: tool, argumentsJson: JSON.stringify(args), timeoutMs: BigInt(timeoutMs) })) {
    out.push(ev);
    if (ev.event.case === "elicitation") await answer(ev.event.value.id);
  }
  return out;
}

const resultText = (evs: CallToolResponse[]) => {
  const r = evs.find((e) => e.event.case === "result");
  if (r?.event.case !== "result") throw new Error("no result");
  return (JSON.parse(r.event.value.resultJson) as { content: { text: string }[] }).content[0]!.text;
};

describe("elicitation through the proxy", () => {
  it("reaches the caller on the call's stream, and the answer reaches the upstream", async () => {
    const s = await server();
    const evs = await runCall(A, s.id, "ask", {}, (id) => A.respondToElicitation({ id, action: "accept", contentJson: JSON.stringify({ colour: "blue" }) }));
    const e = evs.find((x) => x.event.case === "elicitation");
    if (e?.event.case !== "elicitation") throw new Error("no elicitation");
    expect(e.event.value.message).toBe("Which colour?");
    expect(e.event.value.mode).toBe("form");
    expect(JSON.parse(e.event.value.requestedSchemaJson).required).toEqual(["colour"]);
    expect(resultText(evs)).toBe('accept:{"colour":"blue"}');
  });

  it("passes on a decline", async () => {
    const s = await server();
    const evs = await runCall(A, s.id, "ask", {}, (id) => A.respondToElicitation({ id, action: "decline" }));
    expect(resultText(evs)).toBe("decline:{}");
  });

  it("does not let another user answer, and rejects bad answers", async () => {
    const s = await server();
    let foreign: Code | undefined;
    let invalid: Code | undefined;
    const evs = await runCall(A, s.id, "ask", {}, async (id) => {
      foreign = await code(B.respondToElicitation({ id, action: "accept", contentJson: "{}" }));
      invalid = await code(A.respondToElicitation({ id, action: "maybe" }));
      await A.respondToElicitation({ id, action: "cancel" });
    });
    expect(foreign).toBe(Code.NotFound);
    expect(invalid).toBe(Code.InvalidArgument);
    expect(resultText(evs)).toBe("cancel:{}");
    expect(await code(A.respondToElicitation({ id: "mel_gone", action: "cancel" }))).toBe(Code.NotFound);
  });

  it("does not count the wait for the user against the call's time limit", async () => {
    const s = await server();
    // The limit is shorter than the (deliberately) slow answer; only a paused timer lets the call finish.
    const evs = await runCall(
      A,
      s.id,
      "ask",
      {},
      async (id) => {
        await new Promise((r) => setTimeout(r, 1200));
        await A.respondToElicitation({ id, action: "decline" });
      },
      800,
    );
    expect(resultText(evs)).toBe("decline:{}");
  });

  it("enforces the time limit otherwise", async () => {
    const s = await server({ toolTimeoutMs: 100n });
    expect((await A.getServer({ serverId: s.id })).server!.toolTimeoutMs).toBe(100n);
    const err = await runCall(A, s.id, "slow", { steps: 50, delayMs: 100 }, async () => {}).then(
      () => undefined,
      (e) => e as ConnectError,
    );
    expect(err?.message).toContain("did not answer within");
  });
});

describe("list changes and what the upstream says about itself", () => {
  it("passes list_changed on to a running call", async () => {
    const s = await server();
    // Notifications travel on their own stream, which the client opens in the background and which can land after the call it
    // raced has ended; a later call picks them up. Under a full test run that can take a while.
    const kinds = new Set<ListChanged_Kind>();
    for (const deadline = Date.now() + 30_000; Date.now() < deadline && kinds.size < 3; ) {
      const evs = await runCall(A, s.id, "announce", {}, async () => {});
      for (const e of evs) if (e.event.case === "listChanged") kinds.add(e.event.value.kind);
      if (kinds.size < 3) await new Promise((r) => setTimeout(r, 100));
    }
    expect([...kinds].sort()).toEqual([ListChanged_Kind.TOOLS, ListChanged_Kind.PROMPTS, ListChanged_Kind.RESOURCES].sort());
  });

  it("answers GetServerInfo", async () => {
    const s = await server();
    const info = await A.getServerInfo({ serverId: s.id });
    expect(info.name).toBe("fixture");
    expect(info.instructions).toBe("Use echo for echoing.");
    expect(info.hasTools && info.hasPrompts && info.hasResources).toBe(true);
  });

  it("puts instructions and capabilities in the catalog", async () => {
    const s = await server({}, B);
    const cat = await B.getToolCatalog({});
    const cs = cat.catalog!.servers.find((x) => x.serverId === s.id)!;
    expect(cs.instructions).toBe("Use echo for echoing.");
    expect(cs.hasResources && cs.hasPrompts).toBe(true);
    // Again, from the cache and from the stored snapshot.
    const again = await B.getToolCatalog({ version: cat.catalog!.version });
    expect(again.catalog!.servers.find((x) => x.serverId === s.id)!.instructions).toBe("Use echo for echoing.");
  });
});

describe("tool lists of a server", () => {
  it("hides and refuses what the server's manager does not allow", async () => {
    const s = await server({ allowedTools: ["e*", "peek"], deniedTools: ["peek"] }, R);
    const names = (await R.listTools({ serverId: s.id })).tools.map((t) => t.name);
    expect(names).toEqual(["echo"]);
    const cat = await R.getToolCatalog({});
    expect(cat.catalog!.servers.find((x) => x.serverId === s.id)!.tools.map((t) => t.name)).toEqual(["echo"]);
    expect(await code(runCall(R, s.id, "peek", {}, async () => {}))).toBe(Code.PermissionDenied);
    const ok = await runCall(R, s.id, "echo", { text: "x" }, async () => {});
    expect(resultText(ok)).toBe("echo: x");
  });

  it("refuses a headers helper: it would run on the server", async () => {
    expect(
      await code(A.createServer({ server: { name: "helper", scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: `${up.url}/mcp`, headersHelper: "print-token" } } })),
    ).toBe(Code.InvalidArgument);
  });

  it("says who may edit a server", async () => {
    const org = await R.createServer({ server: { name: `org${++n}`, scope: Scope.ORG, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: `${up.url}/mcp` } } });
    const seen = await A.getServer({ serverId: org.server!.id });
    expect(seen.server!.canEdit).toBe(false);
    expect(org.server!.canEdit).toBe(true);
    expect((await A.listServers({})).canManageOrg).toBe(false);
    expect((await R.listServers({})).canManageOrg).toBe(true);
    const mine = await A.createServer({ server: { name: `mine${++n}`, scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: `${up.url}/mcp` } } });
    expect(mine.server!.canEdit).toBe(true);
  });
});

describe("audit log", () => {
  it("is for admins, pages, filters, and carries no arguments", async () => {
    const s = await server({}, R);
    for (const text of ["super-secret-argument-1", "super-secret-argument-2", "super-secret-argument-3"]) {
      await runCall(R, s.id, "echo", { text }, async () => {});
    }
    expect(await code(A.listAudit({}))).toBe(Code.PermissionDenied);
    const first = await R.listAudit({ serverId: s.id, limit: 2 });
    expect(first.entries).toHaveLength(2);
    expect(first.nextCursor).not.toBe("");
    const e = first.entries[0]!;
    expect(e.action).toBe("call_tool");
    expect(e.tool).toBe("echo");
    expect(e.serverName).toBe(s.name);
    expect(e.isError).toBe(false);
    const second = await R.listAudit({ serverId: s.id, limit: 2, cursor: first.nextCursor });
    expect(second.entries).toHaveLength(1);
    expect(second.nextCursor).toBe("");
    const ids = [...first.entries, ...second.entries].map((x) => x.id);
    expect(new Set(ids).size).toBe(3);
    expect(JSON.stringify([first.entries, second.entries], (_, v) => (typeof v === "bigint" ? Number(v) : v))).not.toContain("super-secret-argument");
    expect((await R.listAudit({ serverId: s.id, action: "stdio_launch" })).entries).toHaveLength(0);
    expect(await code(R.listAudit({ cursor: "bm90LWEtY3Vyc29y" }))).toBe(Code.InvalidArgument);
  });

  it("does not keep a server's secrets in the error text", async () => {
    const s = (
      await R.createServer({
        server: { name: `sec${++n}`, scope: Scope.USER, transport: Transport.STREAMABLE_HTTP, enabled: true, remote: { url: `http://127.0.0.1:9/mcp`, headers: { "x-token": "${secret:TOKEN}" } } },
        secrets: { set: { TOKEN: "hunter2-value" } },
      })
    ).server!;
    const p = await adminApi(ts.url, admin.accessToken, "PATCH", `/api/admin/mcp/servers/${s.id}`, { allow_private_network: true });
    expect(p.status).toBe(200);
    await runCall(R, s.id, "echo", { text: "x" }, async () => {}).catch(() => {});
    const log = await R.listAudit({ serverId: s.id });
    expect(log.entries.length).toBeGreaterThan(0);
    expect(log.entries[0]!.isError).toBe(true);
    expect(log.entries[0]!.errorMessage).not.toContain("hunter2-value");
  });
});
