import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { ApiError } from "../src/lib/errors.ts";
import { SearxngProvider } from "../src/search/searxng.ts";
import { filterAndDedupe, normalizeDomains, SearchRegistry } from "../src/search/service.ts";

interface Seen {
  path: string;
  params: URLSearchParams;
}

let server: Server;
let base = "";
let seen: Seen[] = [];
let reply: { status: number; body: unknown } = { status: 200, body: { results: [] } };

beforeAll(async () => {
  server = createServer((req, res) => {
    const u = new URL(req.url ?? "/", "http://x");
    seen.push({ path: u.pathname, params: u.searchParams });
    if (u.pathname === "/healthz") {
      res.writeHead(200).end("OK");
      return;
    }
    res.writeHead(reply.status, { "content-type": "application/json" }).end(JSON.stringify(reply.body));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(() => new Promise<void>((r) => server.close(() => r())));

beforeEach(() => {
  seen = [];
  reply = { status: 200, body: { results: [] } };
});

function registry() {
  return new SearchRegistry([new SearxngProvider({ baseUrl: base })], "searxng");
}

describe("SearXNG provider", () => {
  it("queries the JSON API and maps results", async () => {
    reply.body = {
      results: [
        { title: " Go 1.26 release notes ", url: "https://go.dev/doc/go1.26", content: "What's new", publishedDate: "2026-02-10T00:00:00" },
        { title: "Not http", url: "ftp://example.com/file", content: "skipped" },
      ],
    };
    const out = await registry().search({ query: "go 1.26 release notes" });
    expect(out.provider).toBe("searxng");
    expect(out.results).toHaveLength(1);
    expect(out.results[0]).toMatchObject({ title: "Go 1.26 release notes", url: "https://go.dev/doc/go1.26", snippet: "What's new" });
    expect(out.results[0]!.publishedAt).toBe("2026-02-10T00:00:00.000Z");
    expect(seen[0]!.path).toBe("/search");
    expect(seen[0]!.params.get("format")).toBe("json");
    expect(seen[0]!.params.get("q")).toBe("go 1.26 release notes");
  });

  it("pushes a single allowed domain down as site: and post-filters", async () => {
    reply.body = {
      results: [
        { title: "a", url: "https://docs.github.com/x" },
        { title: "b", url: "https://example.com/y" },
        { title: "c", url: "https://github.com/z" },
      ],
    };
    const out = await registry().search({ query: "actions cache", allowedDomains: ["https://GitHub.com/"] });
    expect(seen[0]!.params.get("q")).toBe("actions cache site:github.com");
    expect(out.results.map((r) => r.title)).toEqual(["a", "c"]);
  });

  it("maps errors", async () => {
    reply = { status: 403, body: {} };
    await expect(registry().search({ query: "x" })).rejects.toMatchObject({ type: "upstream" });
    reply = { status: 429, body: {} };
    await expect(registry().search({ query: "x" })).rejects.toMatchObject({ type: "rate_limit" });
    const down = new SearchRegistry([new SearxngProvider({ baseUrl: "http://127.0.0.1:1" })], "searxng");
    await expect(down.search({ query: "x" })).rejects.toBeInstanceOf(ApiError);
  });

  it("reports health", async () => {
    expect(await registry().status()).toEqual([{ id: "searxng", displayName: "SearXNG", healthy: true }]);
    const down = new SearchRegistry([new SearxngProvider({ baseUrl: "http://127.0.0.1:1" })], "searxng");
    expect((await down.status())[0]!.healthy).toBe(false);
  });
});

describe("search service rules", () => {
  it("validates input", async () => {
    await expect(registry().search({ query: "  " })).rejects.toMatchObject({ type: "invalid_request" });
    await expect(registry().search({ query: "x", allowedDomains: ["a.com"], blockedDomains: ["b.com"] })).rejects.toMatchObject({
      type: "invalid_request",
    });
    await expect(registry().search({ query: "x", provider: "nope" })).rejects.toMatchObject({ type: "invalid_request" });
  });

  it("caps max results", async () => {
    reply.body = { results: Array.from({ length: 40 }, (_, i) => ({ title: `r${i}`, url: `https://e.com/${i}` })) };
    expect((await registry().search({ query: "x" })).results).toHaveLength(10);
    expect((await registry().search({ query: "x", maxResults: 100 })).results).toHaveLength(25);
    expect((await registry().search({ query: "x", maxResults: 3 })).results).toHaveLength(3);
  });

  it("normalizes domains", () => {
    expect(normalizeDomains([" https://Docs.Rs/foo ", "*.example.com", "example.com", ""])).toEqual(["docs.rs", "example.com"]);
    expect(() => normalizeDomains(["bad domain"])).toThrow(ApiError);
  });

  it("filters blocked domains and dedupes by URL without fragment", () => {
    const hits = [
      { title: "1", url: "https://a.com/x#top", snippet: "" },
      { title: "2", url: "https://a.com/x", snippet: "" },
      { title: "3", url: "https://ads.b.com/", snippet: "" },
      { title: "4", url: "not a url", snippet: "" },
    ];
    expect(filterAndDedupe(hits, [], ["b.com"]).map((h) => h.title)).toEqual(["1"]);
  });
});
