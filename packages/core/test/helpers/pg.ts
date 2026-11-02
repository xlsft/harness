/**
 * Throwaway databases on a real Postgres server (TEST_DATABASE_URL, whose role needs CREATEDB), so test
 * runs never see each other's rows and leave nothing behind.
 */
import { randomBytes } from "node:crypto";
import pg from "pg";

export interface TempDatabase {
  url: string;
  drop(): Promise<void>;
}

export async function createTempDatabase(serverUrl: string): Promise<TempDatabase> {
  const name = `harness_test_${Date.now().toString(36)}_${randomBytes(4).toString("hex")}`;
  const admin = new pg.Client({ connectionString: serverUrl });
  await admin.connect();
  try {
    await admin.query(`CREATE DATABASE "${name}"`);
  } finally {
    await admin.end();
  }
  const u = new URL(serverUrl);
  u.pathname = `/${name}`;
  return {
    url: u.toString(),
    drop: async () => {
      const c = new pg.Client({ connectionString: serverUrl });
      await c.connect();
      try {
        await c.query(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
      } finally {
        await c.end();
      }
    },
  };
}
