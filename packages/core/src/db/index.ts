import { closeSync, mkdirSync, openSync, readFileSync, rmSync, writeSync } from "node:fs";
import { PGlite } from "@electric-sql/pglite";
import type { PgDatabase, PgQueryResultHKT } from "drizzle-orm/pg-core";
import { drizzle as drizzleNodePg } from "drizzle-orm/node-postgres";
import { drizzle as drizzlePglite } from "drizzle-orm/pglite";
import pg from "pg";
import { config, type DbConfig } from "../env.ts";
import { runMigrations, type InTransaction } from "./migrate.ts";
import * as schema from "./schema.ts";

/**
 * Drizzle database, whichever driver backs it: node-postgres (DATABASE_URL) or embedded PGlite. Both are
 * the pg dialect, so services only use this type and never touch the driver.
 */
export type Db = PgDatabase<PgQueryResultHKT, typeof schema>;

interface Handle {
  db: Db;
  inTransaction: InTransaction;
  close(): Promise<void>;
}

let handle: Handle | undefined;
let ready: Promise<void> | undefined;

/** Opens the driver selected by `cfg`. Exported for tests that need a second, independent connection. */
export function openDb(cfg: DbConfig): Handle {
  if (cfg.driver === "postgres") {
    const pool = new pg.Pool({ connectionString: cfg.url, max: 10, idleTimeoutMillis: 30_000, application_name: "harness-core" });
    // An idle client losing its connection (server restart) must not crash the process; the pool replaces it.
    pool.on("error", (err) => console.error("[harness] postgres pool error", err.message));
    return {
      db: drizzleNodePg(pool, { schema }) as unknown as Db,
      inTransaction: async (fn) => {
        const client = await pool.connect();
        try {
          await client.query("BEGIN");
          const r = await fn((sql, params) => client.query(sql, params as unknown[]));
          await client.query("COMMIT");
          return r;
        } catch (err) {
          await client.query("ROLLBACK").catch(() => {});
          throw err;
        } finally {
          client.release();
        }
      },
      close: () => pool.end(),
    };
  }
  const unlock = cfg.dataDir === "memory://" ? () => {} : lockDataDir(cfg.dataDir);
  // One embedded Postgres backend in this process. It runs one statement at a time, and its transaction()
  // holds an exclusive lock, so drizzle transactions and migrations cannot interleave with other queries.
  const client = new PGlite(cfg.dataDir);
  return {
    db: drizzlePglite(client, { schema }) as unknown as Db,
    inTransaction: (fn) =>
      client.transaction((tx) => fn((sql, params) => tx.query<Record<string, unknown>>(sql, params as unknown[]))),
    close: async () => {
      try {
        await client.close();
      } finally {
        unlock();
      }
    },
  };
}

/**
 * PGlite is single-process: a second process opening the same directory hangs or corrupts it. Take a pid
 * lock file next to the directory and fail fast with a clear message instead (stale locks are taken over).
 */
function lockDataDir(dir: string): () => void {
  mkdirSync(dir, { recursive: true });
  const file = `${dir}.lock`;
  for (let attempt = 0; ; attempt++) {
    try {
      const fd = openSync(file, "wx");
      writeSync(fd, String(process.pid));
      closeSync(fd);
      break;
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code !== "EEXIST" || attempt > 0) throw err;
      const pid = Number.parseInt(readFileSync(file, "utf8"), 10);
      if (pid && pid !== process.pid && isAlive(pid)) {
        throw new Error(`The embedded database ${dir} is in use by process ${pid}. Stop it first, or set DATABASE_URL to a Postgres server.`);
      }
      rmSync(file, { force: true });
    }
  }
  const release = () => rmSync(file, { force: true });
  process.once("exit", release);
  return () => {
    process.off("exit", release);
    release();
  };
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return (err as NodeJS.ErrnoException).code === "EPERM";
  }
}

function current(): Handle {
  handle ??= openDb(config().db);
  return handle;
}

/** The database accessor. Call `ensureDb()` first (once per request path) so migrations have run. */
export function getDb(): Db {
  return current().db;
}

/** Applies pending migrations once per process. Safe to call concurrently (and across processes). */
export function ensureDb(): Promise<void> {
  ready ??= runMigrations(current().inTransaction).then(
    () => undefined,
    (err) => {
      ready = undefined;
      throw err;
    },
  );
  return ready;
}

/** Closes the connection pool / embedded database; the next getDb() reopens with the current config. */
export async function closeDb(): Promise<void> {
  const h = handle;
  handle = undefined;
  ready = undefined;
  await h?.close();
}

export { schema };
