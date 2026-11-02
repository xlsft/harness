import { migrations as embedded } from "./migrations.generated.ts";

/** Runs a raw statement on the connection that owns the current transaction. */
export type Query = (sql: string, params?: unknown[]) => Promise<{ rows: Record<string, unknown>[] }>;

/** Runs `fn` inside one transaction on one connection (node-postgres client or PGlite). */
export type InTransaction = <T>(fn: (q: Query) => Promise<T>) => Promise<T>;

export interface Migration {
  id: string;
  statements: string[];
}

/** Arbitrary constant naming the migration lock (pg_advisory_xact_lock key space is per database). */
export const MIGRATION_LOCK_KEY = "7378461802946314851";

/**
 * Applies pending migrations in a single transaction holding a transaction-scoped advisory lock, so
 * concurrent startups (several processes or replicas against one database) serialize: the first applies
 * everything, the others wait for its commit and then find nothing to do. Postgres DDL is transactional,
 * so a failed migration leaves the database untouched. Works on node-postgres and PGlite alike.
 * Returns the ids applied by this call.
 */
export async function runMigrations(inTransaction: InTransaction, migrations: Migration[] = embedded): Promise<string[]> {
  return inTransaction(async (q) => {
    await q("SELECT pg_advisory_xact_lock($1::bigint)", [MIGRATION_LOCK_KEY]);
    await q(
      "CREATE TABLE IF NOT EXISTS __harness_migrations (id text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())",
    );
    const applied = new Set((await q("SELECT id FROM __harness_migrations")).rows.map((r) => String(r.id)));
    const done: string[] = [];
    for (const m of migrations) {
      if (applied.has(m.id)) continue;
      for (const stmt of m.statements) await q(stmt);
      await q("INSERT INTO __harness_migrations (id) VALUES ($1)", [m.id]);
      done.push(m.id);
    }
    return done;
  });
}
