/**
 * The same server on a real Postgres instead of PGlite: driver wiring, concurrent migrations, column
 * types, int8 versions, concurrent sync writes, ILIKE search, aggregates and transactions.
 *
 * Runs only when TEST_DATABASE_URL points at a server whose role may CREATE DATABASE, e.g.
 *   docker run --rm -d -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:18-alpine
 *   TEST_DATABASE_URL=postgres://postgres:test@localhost:55432/postgres pnpm test test/postgres.test.ts
 * Every run uses throwaway databases. (TEST_ALL_ON_POSTGRES=1 additionally runs every other suite there.)
 */
import { create } from "@bufbuild/protobuf";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { migrations } from "../src/db/migrations.generated.ts";
import { SessionInfoSchema } from "../src/gen/harness/agent/v1/agent_pb.ts";
import { Scope } from "../src/gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../src/gen/harness/sync/v1/sync_pb.ts";
import { createTempDatabase, type TempDatabase } from "./helpers/pg.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const SERVER = process.env.TEST_DATABASE_URL;

describe.skipIf(!SERVER)("real Postgres (TEST_DATABASE_URL)", () => {
  let tmp: TempDatabase;
  let ts: TestServer;
  let admin: Awaited<ReturnType<typeof deviceLogin>>;
  let alice: Awaited<ReturnType<typeof deviceLogin>>;

  beforeAll(async () => {
    tmp = await createTempDatabase(SERVER!);
    ts = await startTestServer({ DATABASE_URL: tmp.url });
    admin = await deviceLogin(ts);
    alice = await createUserAndLogin(ts, admin.cookie, "alice@pg.test");
  });

  afterAll(async () => {
    await ts?.close();
    await tmp?.drop();
  });

  it("serializes concurrent migrations from independent pools (pg_advisory_xact_lock)", async () => {
    const fresh = await createTempDatabase(SERVER!);
    const { openDb } = await import("../src/db/index.ts");
    const { runMigrations } = await import("../src/db/migrate.ts");
    const handles = Array.from({ length: 4 }, () => openDb({ driver: "postgres", url: fresh.url }));
    try {
      const results = await Promise.all(handles.map((h) => runMigrations(h.inTransaction)));
      expect(results.filter((r) => r.length > 0)).toHaveLength(1);
      expect(results.flat()).toEqual(migrations.map((m) => m.id));
      expect(await runMigrations(handles[0]!.inTransaction)).toEqual([]);
    } finally {
      await Promise.all(handles.map((h) => h.close()));
      await fresh.drop();
    }
  });

  it("creates the pg column types", async () => {
    const { getDb } = await import("../src/db/index.ts");
    const { sql } = await import("drizzle-orm");
    // Db is driver-agnostic, so execute() is untyped; node-postgres returns { rows }.
    const res = (await getDb().execute(sql`
      select table_name, column_name, data_type from information_schema.columns
      where table_schema = 'public' and (table_name, column_name) in (
        ('sync_document', 'version'), ('sync_counter', 'value'), ('sync_session_entry', 'seq'),
        ('user', 'email_verified'), ('model', 'capabilities'), ('usage_record', 'cost_usd'), ('session', 'expires_at'))`)) as unknown as {
      rows: { table_name: string; column_name: string; data_type: string }[];
    };
    const types = Object.fromEntries(res.rows.map((r) => [`${r.table_name}.${r.column_name}`, r.data_type]));
    expect(types).toEqual({
      "sync_document.version": "bigint",
      "sync_counter.value": "bigint",
      "sync_session_entry.seq": "bigint",
      "user.email_verified": "boolean",
      "model.capabilities": "jsonb",
      "usage_record.cost_usd": "double precision",
      "session.expires_at": "timestamp with time zone",
    });
  });

  it("assigns unique, ordered versions to concurrent writes and keeps CAS", async () => {
    const A = ts.as(alice.accessToken).sync;
    const puts = await Promise.all(
      Array.from({ length: 25 }, (_, i) => A.putDocument({ ref: { scope: Scope.USER, kind: DocumentKind.RULE, key: `r/${i}.md` }, content: `rule ${i}` })),
    );
    const versions = puts.map((p) => (p.result.case === "document" ? p.result.value.version : -1n));
    expect(new Set(versions).size).toBe(25);
    const feed = await A.listChanges({ sinceVersion: 0n, pageSize: 1000 });
    const fv = feed.changes.map((c) => c.change.value!.version);
    expect(fv).toEqual([...fv].sort((a, b) => (a < b ? -1 : 1)));
    expect(feed.nextVersion).toBe(fv.at(-1));

    const first = puts[0]!.result.value as { version: bigint; id: string };
    const stale = await A.putDocument({ ref: { scope: Scope.USER, kind: DocumentKind.RULE, key: "r/0.md" }, content: "x", baseVersion: first.version + 1000n });
    expect(stale.result.case).toBe("conflict");
    const del = await A.deleteDocument({ id: first.id, baseVersion: first.version });
    expect(del.result.case).toBe("tombstone");
  });

  it("carries versions beyond int32 through int8 columns", async () => {
    const { getDb, schema } = await import("../src/db/index.ts");
    const { eq } = await import("drizzle-orm");
    const big = 2 ** 31 + 10;
    await getDb().update(schema.syncCounter).set({ value: big }).where(eq(schema.syncCounter.name, "documents"));
    const A = ts.as(alice.accessToken).sync;
    const r = await A.putDocument({ ref: { scope: Scope.USER, kind: DocumentKind.MEMORY, key: "user" }, content: "big" });
    expect(r.result.case === "document" && r.result.value.version).toBe(BigInt(big + 1));
    const feed = await A.listChanges({ sinceVersion: BigInt(big) });
    expect(feed.changes.map((c) => c.change.value!.version)).toEqual([BigInt(big + 1)]);
    expect(feed.nextVersion).toBe(BigInt(big + 1));
  });

  it("appends transcripts in a transaction and searches with escaped ILIKE", async () => {
    const A = ts.as(alice.accessToken).sync;
    const sid = "0b5a2c1e-1111-4222-8333-444455556677";
    await A.upsertSession({ session: { info: create(SessionInfoSchema, { id: sid, title: "Fix 100% CPU in Parser_Main" }) } });
    const r = await A.appendSessionEvents({
      sessionId: sid,
      entries: [
        { uuid: "u1", entryJson: JSON.stringify({ type: "user", message: { content: "Why is the ÜBER cache cold?" } }) },
        { uuid: "u1", entryJson: "{}" },
        { uuid: "u2", entryJson: "{}" },
      ],
    });
    expect([r.accepted, r.duplicates, Number(r.lastSeq)]).toEqual([2, 1, 2]);
    const ids = async (query: string) => (await A.listSessions({ query })).sessions.map((s) => s.info!.id);
    expect(await ids("100%")).toEqual([sid]);
    expect(await ids("parser_main")).toEqual([sid]);
    expect(await ids("parserxmain")).toEqual([]); // "_" is literal
    expect(await ids("über CACHE")).toEqual([sid]); // case-insensitive, non-ASCII, transcript text
    expect(await ids("10%%")).toEqual([]);
  });

  it("sums spend with coalesce(sum()) over double precision", async () => {
    const { getDb, schema } = await import("../src/db/index.ts");
    const { spendStatus } = await import("../src/services/quotas.ts");
    const user = { id: alice.user!.id, email: "alice@pg.test", name: "alice", role: "user" as const };
    expect((await spendStatus(user)).daily_usd).toBe(0);
    const now = new Date();
    await getDb()
      .insert(schema.usageRecord)
      .values([0.125, 0.25].map((costUsd, i) => ({ id: `use_pg_${i}`, userId: user.id, modelId: "m", costUsd, status: "ok", createdAt: now })));
    const s = await spendStatus(user);
    expect(s.daily_usd).toBeCloseTo(0.375, 10);
    expect(s.monthly_usd).toBeCloseTo(0.375, 10);
  });
});
