/**
 * GitHub Issues on a fake GitHub Enterprise Server (/api/v3), signed in through a code host connection: search keeps pull
 * requests out, changes map to the REST calls, a missing issue and a GitLab connection read as a tracker's errors.
 */
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { GITHUB, githubSearchQuery, parseGitHubId } from "../src/tracker/github.ts";
import type { OpenOptions, VcsSignIn } from "../src/tracker/provider.ts";
import { trackerModule } from "../src/tracker/registry.ts";

const item = (n: number, pr = false) => ({
  number: n,
  title: "Issue",
  body: "Body",
  html_url: `https://ghe.test/octo/app/issues/${n}`,
  state: "open",
  updated_at: "2026-01-02T03:04:05Z",
  repository_url: "https://ghe.test/api/v3/repos/octo/app",
  labels: [{ name: "bug" }],
  assignees: [{ login: "alice" }],
  ...(pr ? { pull_request: { url: "x" } } : {}),
});

describe("GitHub Issues", () => {
  const seen = { queries: [] as string[], patches: [] as Record<string, unknown>[], added: [] as string[], removed: [] as string[], posted: [] as string[] };
  let server: Server;
  let url = "";

  beforeAll(async () => {
    server = createServer(async (req, res) => {
      const send = (status: number, body?: unknown) => {
        res.writeHead(status, { "content-type": "application/json" });
        res.end(body === undefined ? "" : JSON.stringify(body));
      };
      if (req.headers.authorization !== "Bearer gh-token") return send(401, { message: "Bad credentials" });
      let raw = "";
      for await (const c of req) raw += c;
      const body = raw ? JSON.parse(raw) : {};
      const u = new URL(req.url ?? "/", "http://x");
      const p = u.pathname.replace(/^\/api\/v3\//, "");
      const m = req.method;
      if (p === "installation/repositories") return send(200, { repositories: [{ full_name: "octo/app", name: "app", has_issues: true }, { full_name: "octo/old", name: "old", archived: true }] });
      if (p === "search/issues") {
        seen.queries.push(u.searchParams.get("q") ?? "");
        return send(200, { total_count: 3, items: [item(1), item(2, true), item(3)] });
      }
      if (p === "repos/octo/app/issues/1" && m === "GET") return send(200, item(1));
      if (p === "repos/octo/app/issues/1" && m === "PATCH") {
        seen.patches.push(body);
        return send(200, item(1));
      }
      if (p === "repos/octo/app/issues/1/comments" && m === "GET") return send(200, [{ body: "hi", created_at: "2026-01-02T03:04:05Z", user: { login: "bob" } }]);
      if (p === "repos/octo/app/issues/1/comments") {
        seen.posted.push(body.body);
        return send(201, { id: 1 });
      }
      if (p === "repos/octo/app/issues/1/labels" && m === "POST") {
        seen.added.push(...body.labels);
        return send(200, []);
      }
      if (p.startsWith("repos/octo/app/issues/1/labels/") && m === "DELETE") {
        const l = decodeURIComponent(p.split("/").pop()!);
        seen.removed.push(l);
        return send(l === "gone" ? 404 : 200, l === "gone" ? { message: "Not Found" } : []);
      }
      if (p === "repos/octo/app/milestones") return send(200, [{ number: 4, title: "v1" }]);
      if (p === "repos/octo/app/labels") return send(200, [{ name: "bug" }, { name: "review" }]);
      if (p === "repos/octo/app/assignees") return send(200, [{ login: "alice" }]);
      if (p === "repos/octo/app/issues" && m === "POST") return send(201, item(7));
      return send(404, { message: "Not Found" });
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });
  afterAll(() => new Promise<void>((r) => server.close(() => r())));

  const signIn = (o: Partial<VcsSignIn> = {}) => async (): Promise<VcsSignIn> => ({ type: "github", baseUrl: url, token: async () => "gh-token", ...o });
  const opts = (vcs = signIn()): OpenOptions => ({ baseUrl: "https://github.com", auth: "vcs", login: "", oauthClientId: "", credentials: {}, options: {}, allowPrivateNetwork: true, retryDelaysMs: [], vcs });

  it("is registered and signs in through a code host connection only", () => {
    expect(trackerModule("github")).toBe(GITHUB);
    expect(GITHUB.capabilities.authMethods).toEqual(["vcs"]);
    expect(() => GITHUB.open({ ...opts(), auth: "token" })).toThrow(/does not sign in/);
  });

  it("reads ids and keeps pull requests out of a search unless asked", () => {
    expect(parseGitHubId("octo/app#12")).toMatchObject({ repo: "octo/app", number: 12 });
    expect(() => parseGitHubId("nonsense")).toThrow();
    expect(githubSearchQuery("is:pr author:me")).toBe("is:pr author:me");
  });

  it("searches, reads, changes, comments and creates", async () => {
    const gh = GITHUB.open(opts());
    expect((await gh.test()).login).toBe("GitHub App");
    const found = await gh.search("repo:octo/app is:open", { limit: 10 });
    expect(found.issues.map((i) => i.id)).toEqual(["octo/app#1", "octo/app#3"]);
    expect(seen.queries[0]).toBe("repo:octo/app is:open is:issue");
    const one = await gh.get("octo/app#1");
    expect(one.project).toBe("octo/app");
    expect(one.comments[0]?.author).toBe("bob");
    await expect(gh.get("octo/app#404")).rejects.toMatchObject({ kind: "not_found" });

    await gh.update("octo/app#1", { fields: [{ name: "State", value: "closed as not planned" }, { name: "Add labels", value: "review" }, { name: "Remove labels", value: "bug, gone" }, { name: "Assignees", value: "me, bob" }, { name: "Milestone", value: "v1" }] });
    expect(seen.patches[0]).toEqual({ state: "closed", state_reason: "not_planned", assignees: ["bob"], milestone: 4 });
    expect(seen.added).toEqual(["review"]);
    expect(seen.removed).toEqual(["bug", "gone"]);
    await expect(gh.update("octo/app#1", { fields: [{ name: "State", value: "limbo" }] })).rejects.toMatchObject({ kind: "other" });

    await gh.comment("octo/app#1", "shipped");
    expect(seen.posted).toEqual(["shipped"]);
    expect((await gh.create({ project: "octo/app", title: "New" })).id).toBe("octo/app#7");
    const fields = await gh.fields("octo/app");
    expect(fields[1]?.values).toEqual(["bug", "review"]);
    expect(fields[5]?.values).toEqual(["v1"]);
    expect((await gh.projects()).map((p) => p.key)).toEqual(["octo/app"]);
  });

  it("reads a bad token and a GitLab connection as a tracker's errors", async () => {
    await expect(GITHUB.open(opts(signIn({ token: async () => "wrong" }))).test()).rejects.toMatchObject({ kind: "auth" });
    await expect(GITHUB.open(opts(signIn({ type: "gitlab" }))).test()).rejects.toMatchObject({ kind: "other" });
  });
});
