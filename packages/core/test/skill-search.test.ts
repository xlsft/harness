/**
 * External skill search against a fake skills.sh: the token stays here, answers are cached, the source hides without a token, and the
 * organization's policy stops it on the server too.
 */
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { resetSkillSearch } from "../src/skills/skillssh.ts";
import { deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

let ts: TestServer;
let admin: ReturnType<TestServer["as"]>;
let upstream: Server;
const requests: { url: string; auth: string }[] = [];
let rateLimited = false;

const skill = { id: "acme/skills/pdf", slug: "pdf", name: "PDF", source: "acme/skills", installs: 1234, sourceType: "github", url: "https://skills.sh/acme/skills/pdf" };

beforeAll(async () => {
  upstream = createServer((req, res) => {
    requests.push({ url: req.url ?? "", auth: req.headers.authorization ?? "" });
    const send = (body: unknown) => {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify(body));
    };
    if (rateLimited) {
      res.statusCode = 429;
      res.setHeader("retry-after", "30");
      res.end("{}");
    } else if (req.url?.startsWith("/api/v1/skills/search")) {
      send({ data: [skill] });
    } else if (req.url === "/api/v1/skills/curated") {
      send({ data: [{ owner: "acme", skills: [skill, { ...skill, id: "acme/skills/big", slug: "big", name: "Big", installs: 9999 }] }] });
    } else if (req.url?.startsWith("/api/v1/skills/audit/")) {
      send({ audits: [{ provider: "Socket", status: "warn", summary: "One alert", auditedAt: "2026-11-01T00:00:00Z", riskLevel: "MEDIUM" }] });
    } else if (req.url === "/api/v1/skills/acme/skills/pdf") {
      send({ ...skill, hash: "abc123", files: [{ path: "SKILL.md", contents: "---\nname: PDF\n---\nDo PDFs." }] });
    } else {
      res.statusCode = 404;
      res.end("{}");
    }
  });
  await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
  process.env.SKILLS_SH_URL = `http://127.0.0.1:${(upstream.address() as AddressInfo).port}`;
  ts = await startTestServer();
  admin = ts.as((await deviceLogin(ts)).accessToken);
});

afterAll(async () => {
  await ts.close();
  upstream.close();
  delete process.env.SKILLS_SH_URL;
});

beforeEach(() => {
  process.env.SKILLS_SH_TOKEN = "test-token";
  requests.length = 0;
  rateLimited = false;
  resetSkillSearch();
});

afterEach(async () => {
  delete process.env.SKILLS_SH_TOKEN;
  await admin.policy.setManagedPolicy({ policy: {} });
});

const search = () => admin.skills;

describe("skill search", () => {
  it("is hidden without a token, and asks nobody", async () => {
    delete process.env.SKILLS_SH_TOKEN;
    expect(await search().getSkillSearchStatus({})).toMatchObject({ available: false, reason: "not_configured" });
    await expect(search().searchSkills({ query: "pdf" })).rejects.toMatchObject({ code: Code.FailedPrecondition });
    expect(requests).toEqual([]);
  });

  it("gives what the source says, with its audits, and sends the token upstream only", async () => {
    expect((await search().getSkillSearchStatus({})).available).toBe(true);
    const { skills } = await search().searchSkills({ query: "pdf" });
    expect(skills[0]).toMatchObject({ id: "acme/skills/pdf", name: "PDF", source: "acme/skills", installs: 1234n });
    expect(skills[0]!.audits[0]).toMatchObject({ provider: "Socket", status: "warn", summary: "One alert" });
    expect(requests.every((r) => r.auth === "Bearer test-token")).toBe(true);
    expect(JSON.stringify(skills, (_k, v) => (typeof v === "bigint" ? String(v) : v))).not.toContain("test-token");
  });

  it("caches", async () => {
    await search().searchSkills({ query: "pdf" });
    const after = requests.length;
    await search().searchSkills({ query: "PDF" });
    expect(requests.length).toBe(after);
  });

  it("lists the curated set, most installed first", async () => {
    const { skills } = await search().listCuratedSkills({ limit: 1 });
    expect(skills.map((s) => s.id)).toEqual(["acme/skills/big"]);
  });

  it("gives a skill's files and hash", async () => {
    const snap = await search().getSkillSnapshot({ id: "acme/skills/pdf" });
    expect(snap.hash).toBe("abc123");
    expect(snap.files[0]).toMatchObject({ path: "SKILL.md" });
    expect(snap.skill?.audits[0]?.status).toBe("warn");
  });

  it("refuses an id that would leave the API's paths", async () => {
    for (const id of ["../../etc/passwd", "a", "a/../b", "a b/c"]) {
      await expect(search().getSkillSnapshot({ id })).rejects.toMatchObject({ code: Code.InvalidArgument });
    }
    expect(requests).toEqual([]);
  });

  it("waits when told to", async () => {
    rateLimited = true;
    await expect(search().searchSkills({ query: "first" })).rejects.toMatchObject({ code: Code.Unavailable });
    const after = requests.length;
    await expect(search().searchSkills({ query: "second" })).rejects.toBeInstanceOf(ConnectError);
    expect(requests.length).toBe(after);
  });

  it("is stopped by the organization's policy, on the server", async () => {
    await admin.policy.setManagedPolicy({ policy: { externalSkillSearchDisabled: true } });
    expect(await search().getSkillSearchStatus({})).toMatchObject({ available: false, reason: "policy" });
    await expect(search().searchSkills({ query: "pdf" })).rejects.toMatchObject({ code: Code.PermissionDenied });
    expect(requests).toEqual([]);
  });

  it("keeps the skill lists of the policy", async () => {
    const set = await admin.policy.setManagedPolicy({ policy: { restrictSkills: true, allowedSkills: ["deploy", "kit:*"], blockedSkills: ["bad"] } });
    expect(set.policy).toMatchObject({ restrictSkills: true, allowedSkills: ["deploy", "kit:*"], blockedSkills: ["bad"] });
    const off = await admin.policy.setManagedPolicy({ policy: {} });
    expect(off.policy).toMatchObject({ restrictSkills: false, allowedSkills: [], blockedSkills: [] });
  });
});
