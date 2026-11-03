/**
 * Local models (agent.v1 localmodels.proto) on the server side: the admin's allow switch in the POLICY document,
 * usage reported by clients (cost 0, priced, deduplicated, local ids only), and prompt bundles for local ids.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { isLocalModelId, localModelsAllowedIn, withLocalModelsAllowed } from "../src/services/local-models.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;
let user: ReturnType<TestServer["as"]>;

const POLICY_REF = { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default" };

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

beforeAll(async () => {
  ts = await startTestServer();
  const a = await deviceLogin(ts);
  admin = ts.as(a.accessToken);
  const u = await createUserAndLogin(ts, a.cookie, "local-models@test.local");
  user = ts.as(u.accessToken);
});
afterAll(() => ts.close());

describe("ids and policy content", () => {
  it("recognizes local ids and the switch", () => {
    expect(isLocalModelId("local:ollama/qwen3:8b")).toBe(true);
    expect(isLocalModelId("local:lmstudio/org/model")).toBe(true);
    expect(isLocalModelId("local:ollama")).toBe(false);
    expect(isLocalModelId("glm-4.6")).toBe(false);
    expect(localModelsAllowedIn({})).toBe(true);
    expect(localModelsAllowedIn({ disableLocalModels: true })).toBe(false);
    expect(withLocalModelsAllowed({ a: 1, disableLocalModels: true }, true)).toEqual({ a: 1 });
  });
});

describe("AdminService local model policy", () => {
  it("is admin only, defaults to allowed, and lands in the POLICY document", async () => {
    expect(await code(user.admin.getLocalModelPolicy({}))).toBe(Code.PermissionDenied);
    expect(await code(user.admin.setLocalModelPolicy({ policy: { allowed: false } }))).toBe(Code.PermissionDenied);
    expect((await admin.admin.getLocalModelPolicy({})).policy?.allowed).toBe(true);

    await admin.sync.putDocument({ ref: POLICY_REF, content: JSON.stringify({ availableModels: ["m1"] }) });
    expect((await admin.admin.setLocalModelPolicy({ policy: { allowed: false } })).policy?.allowed).toBe(false);
    const doc = (await user.sync.getDocument({ selector: { case: "ref", value: POLICY_REF } })).document!;
    expect(JSON.parse(doc.content)).toEqual({ availableModels: ["m1"], disableLocalModels: true });

    expect((await admin.admin.setLocalModelPolicy({ policy: { allowed: true } })).policy?.allowed).toBe(true);
    const again = (await user.sync.getDocument({ selector: { case: "ref", value: POLICY_REF } })).document!;
    expect(JSON.parse(again.content)).toEqual({ availableModels: ["m1"] });
  });
});

describe("UsageService.ReportLocalUsage", () => {
  const rec = (id: string, over: Record<string, unknown> = {}) => ({
    id,
    modelId: "local:ollama/qwen3:8b",
    providerId: "ollama",
    requestClass: "main",
    sessionId: "s1",
    inputTokens: 1000n,
    outputTokens: 200n,
    cacheReadTokens: 0n,
    status: "ok",
    stopReason: "end_turn",
    durationMs: 1500n,
    createdAt: new Date(Date.now() - 60_000).toISOString(),
    ...over,
  });

  it("stores records at cost 0, once, and the report marks them local", async () => {
    const r = await user.usage.reportLocalUsage({ records: [rec("lus_aaaaaaaaaaaa"), rec("lus_bbbbbbbbbbbb", { requestClass: "compaction" })] });
    expect(r.accepted).toBe(2);
    expect((await user.usage.reportLocalUsage({ records: [rec("lus_aaaaaaaaaaaa")] })).accepted).toBe(0);

    const report = (await user.usage.getMyUsage({})).report!;
    expect(Number(report.totals?.requests)).toBe(2);
    expect(Number(report.totals?.inputTokens)).toBe(2000);
    expect(report.totals?.costUsd).toBe(0);
    expect(Number(report.totals?.unpricedTokens)).toBe(0);
    const m = report.models.find((x) => x.modelId === "local:ollama/qwen3:8b")!;
    expect(m.local).toBe(true);
    expect(m.priced).toBe(true);
    expect(m.providerId).toBe("local:ollama");
    expect(report.requestClasses.map((c) => c.requestClass).sort()).toEqual(["compaction", "main"]);

    // Another user reporting the same client id does not collide.
    expect((await admin.usage.reportLocalUsage({ records: [rec("lus_aaaaaaaaaaaa")] })).accepted).toBe(1);
  });

  it("refuses non-local ids, big batches and anonymous callers", async () => {
    expect(await code(user.usage.reportLocalUsage({ records: [rec("lus_cccccccccccc", { modelId: "glm-4.6" })] }))).toBe(Code.InvalidArgument);
    const many = Array.from({ length: 501 }, (_, i) => rec(`lus_many_${String(i).padStart(6, "0")}`));
    expect(await code(user.usage.reportLocalUsage({ records: many }))).toBe(Code.InvalidArgument);
    expect(await code(ts.usage.reportLocalUsage({ records: [rec("lus_dddddddddddd")] }))).toBe(Code.Unauthenticated);
  });
});

describe("prompt bundles", () => {
  it("serves a bundle for a local model id", async () => {
    const r = await user.prompts.getBundle({ model: "local:ollama/qwen3:8b" });
    expect(r.bundle?.model).toBe("local:ollama/qwen3:8b");
    expect(r.bundle?.system.length).toBeGreaterThan(0);
  });
});
