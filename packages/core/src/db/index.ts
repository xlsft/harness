import { createClient, type Client } from "@libsql/client";
import { drizzle, type LibSQLDatabase } from "drizzle-orm/libsql";
import { config } from "../env.ts";
import { migrations } from "./migrations.generated.ts";
import * as schema from "./schema.ts";

export type Db = LibSQLDatabase<typeof schema>;

let client: Client | undefined;
let db: Db | undefined;
let ready: Promise<void> | undefined;

/** Drizzle instance (libSQL driver over a local SQLite file by default). */
export function getDb(): Db {
  if (!db) {
    // timeout: busy wait for pooled connections (transactions get their own connection).
    client = createClient({ url: config().dbUrl, timeout: 5000 });
    db = drizzle(client, { schema });
  }
  return db;
}

/** Applies pending migrations once per process. Safe to call concurrently. */
export function ensureDb(): Promise<void> {
  ready ??= migrate().catch((err) => {
    ready = undefined;
    throw err;
  });
  return ready;
}

async function migrate(): Promise<void> {
  getDb();
  const c = client!;
  await c.execute("PRAGMA journal_mode = WAL");
  await c.execute("PRAGMA foreign_keys = ON");
  await c.execute("PRAGMA busy_timeout = 5000");
  await c.execute(
    "CREATE TABLE IF NOT EXISTS __harness_migrations (id TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)",
  );
  const applied = new Set((await c.execute("SELECT id FROM __harness_migrations")).rows.map((r) => String(r.id)));
  for (const m of migrations) {
    if (applied.has(m.id)) continue;
    await c.batch(
      [
        ...m.statements,
        { sql: "INSERT INTO __harness_migrations (id, applied_at) VALUES (?, ?)", args: [m.id, Date.now()] },
      ],
      "write",
    );
  }
}

/** For tests: drop the connection so the next getDb() reopens with the current config. */
export function closeDb(): void {
  client?.close();
  client = undefined;
  db = undefined;
  ready = undefined;
}

export { schema };
