/** Embedded PGlite fallback: file-backed persistence, idempotent migrations, the data dir lock file, upgrades. */
import { existsSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { eq } from "drizzle-orm";
import { describe, expect, it } from "vitest";
import { openDb, schema } from "../src/db/index.ts";
import { runMigrations } from "../src/db/migrate.ts";
import { migrations } from "../src/db/migrations.generated.ts";

describe("PGlite on disk", () => {
  it("persists across reopen, migrates once and releases its lock", async () => {
    const dir = join(mkdtempSync(join(tmpdir(), "harness-pglite-")), "pglite");
    const a = openDb({ driver: "pglite", dataDir: dir });
    expect(existsSync(`${dir}.lock`)).toBe(true);
    expect(await runMigrations(a.inTransaction)).toEqual(migrations.map((m) => m.id));
    await a.db.insert(schema.setting).values({ key: "probe", value: { n: 1, s: "ü" }, updatedAt: new Date(1_700_000_000_123) });
    await a.close();
    expect(existsSync(`${dir}.lock`)).toBe(false);

    const b = openDb({ driver: "pglite", dataDir: dir });
    try {
      expect(await runMigrations(b.inTransaction)).toEqual([]);
      const row = await b.db.query.setting.findFirst({ where: eq(schema.setting.key, "probe") });
      expect(row?.value).toEqual({ n: 1, s: "ü" });
      expect(row?.updatedAt.getTime()).toBe(1_700_000_000_123);
    } finally {
      await b.close();
    }
  });

  it("adds the object store to an existing database without touching what is there", async () => {
    const dir = join(mkdtempSync(join(tmpdir(), "harness-pglite-")), "pglite");
    const before = migrations.filter((m) => m.id < "0010_objects");
    const db = openDb({ driver: "pglite", dataDir: dir });
    try {
      await runMigrations(db.inTransaction, before);
      const now = new Date();
      await db.db.insert(schema.user).values({ id: "u1", name: "Old", email: "old@test.local", emailVerified: true, image: "data:image/png;base64,AAAA", createdAt: now, updatedAt: now });
      expect(await runMigrations(db.inTransaction)).toContain("0010_objects");
      const row = await db.db.query.user.findFirst({ where: eq(schema.user.id, "u1") });
      expect(row?.image).toBe("data:image/png;base64,AAAA");
      expect(await db.db.select().from(schema.storedObject)).toEqual([]);
    } finally {
      await db.close();
    }
  });

  it("moves a tracker connection that served Knowledge into a knowledge source borrowing it", async () => {
    const dir = join(mkdtempSync(join(tmpdir(), "harness-pglite-")), "pglite");
    const before = migrations.filter((m) => m.id < "0018_knowledge_connections");
    const db = openDb({ driver: "pglite", dataDir: dir });
    try {
      await runMigrations(db.inTransaction, before);
      await db.inTransaction(async (q) => {
        await q(
          `INSERT INTO tracker_connection (id, provider, name, base_url, auth, knowledge_enabled, knowledge_query, enabled, created_at, updated_at) VALUES
           ('tc_kb', 'youtrack', 'Company', 'https://yt.test', 'token', true, 'project: HELP', true, now(), now()),
           ('tc_plain', 'youtrack', 'Plain', 'https://yt.test', 'token', false, '', true, now(), now())`,
        );
      });
      expect(await runMigrations(db.inTransaction)).toContain("0018_knowledge_connections");
      const rows = await db.db.select().from(schema.knowledgeConnection);
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ provider: "youtrack-kb", borrowConnectionId: "tc_kb", migratedFrom: "tc_kb", query: "project: HELP", enabled: true, auth: "", secretsEnc: "" });
      expect(rows[0]!.id).toMatch(/^kc_/);
      const tc = await db.db.query.trackerConnection.findFirst({ where: eq(schema.trackerConnection.id, "tc_kb") });
      expect(tc).toMatchObject({ knowledgeEnabled: false, knowledgeQuery: "", options: {} });
    } finally {
      await db.close();
    }
  });
});
