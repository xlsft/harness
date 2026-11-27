/**
 * Linear and Linear documents on a fake GraphQL API: an API key and OAuth refresh, its filter forms, a missing issue, and the
 * documents borrowing the Linear sign-in, read live and narrowed to a project. No real account.
 */
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { LINEAR_DOCS } from "../src/knowledge/linear-docs.ts";
import { LINEAR, LINEAR_OAUTH, parseLinearFilter } from "../src/tracker/linear.ts";
import type { OpenOptions } from "../src/tracker/provider.ts";
import { knowledgeModule, trackerModule } from "../src/tracker/registry.ts";
import { FakeLinear } from "./helpers/fake-trackers.ts";

describe("Linear", () => {
  const linear = new FakeLinear();
  const opts = (o: Partial<OpenOptions>): OpenOptions => ({ baseUrl: linear.url, auth: "token", login: "", oauthClientId: "", credentials: { token: linear.apiKey }, options: {}, allowPrivateNetwork: true, retryDelaysMs: [], ...o });

  beforeAll(() => linear.start());
  afterAll(() => linear.close());

  it("is registered, and its documents only borrow a Linear sign-in", () => {
    expect(trackerModule("linear")).toBe(LINEAR);
    expect(knowledgeModule("linear-docs")).toBe(LINEAR_DOCS);
    expect(LINEAR_DOCS.capabilities.authMethods).toEqual([]);
    expect(LINEAR_DOCS.capabilities.borrowFrom).toEqual(["linear"]);
  });

  it("signs in with an API key, refuses a wrong one, and says when an issue is not there", async () => {
    expect((await LINEAR.open(opts({})).test()).displayName).toBe("Alice");
    await expect(LINEAR.open(opts({ credentials: { token: "wrong" } })).test()).rejects.toMatchObject({ kind: "auth" });
    await expect(LINEAR.open(opts({})).get("ENG-404")).rejects.toMatchObject({ kind: "not_found" });
  });

  it("refreshes an expired OAuth token and keeps it", async () => {
    const stored: unknown[] = [];
    const l = LINEAR.open(opts({ auth: "oauth", oauthClientId: "c", credentials: { accessToken: "old", refreshToken: "lin-refresh", expiresAt: 1 }, onOAuthTokens: async (t) => void stored.push(t) }));
    expect((await l.test()).displayName).toBe("Alice");
    expect(stored).toHaveLength(1);
    const u = new URL(await LINEAR_OAUTH.authorizeUrl({ baseUrl: linear.url, clientId: "c", redirectUri: "http://127.0.0.1:1/cb", state: "s", challenge: "ch", allowPrivateNetwork: true, options: {} }));
    expect(u.searchParams.get("code_challenge_method")).toBe("S256");
  });

  it("reads the short filter form and JSON alike", () => {
    expect(parseLinearFilter('{"team":{"key":{"eq":"ENG"}}}')).toEqual({ team: { key: { eq: "ENG" } } });
    expect(() => parseLinearFilter("colour:red")).toThrow(/Unknown filter/);
  });

  it("serves its documents with the borrowed sign-in, narrowed to a project", async () => {
    linear.documents = [
      { id: "d1", title: "Deploys", content: "canary rollout steps", project: "Handbook" },
      { id: "d2", title: "Oncall", content: "canary alerts", project: "" },
      { id: "d3", title: "Other", content: "nothing", project: "Other" },
    ];
    const docs = LINEAR_DOCS.open({ ...opts({}), borrowedFrom: "linear", trackerBaseUrl: linear.url });
    expect((await docs.searchArticles("canary", "", 5)).map((a) => a.id)).toEqual(["d1", "d2"]);
    expect((await docs.searchArticles("canary", 'project: "handbook"', 5)).map((a) => a.id)).toEqual(["d1"]);
    const page = await docs.listArticles("", "", { limit: 2, offset: 1 });
    expect(page.articles.map((a) => a.id)).toEqual(["d2", "d3"]);
    expect(page.hasMore).toBe(false);
    await expect(docs.getArticle("nope")).rejects.toMatchObject({ kind: "not_found" });
  });
});
