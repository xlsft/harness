import { and, count, eq, isNull, ne, or } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";

/** Admins who can still sign in (not disabled), optionally not counting one user. */
export async function activeAdminCount(exceptUserId?: string): Promise<number> {
  const u = schema.user;
  const active = and(eq(u.role, "admin"), or(isNull(u.banned), eq(u.banned, false)));
  const [row] = await getDb()
    .select({ n: count() })
    .from(u)
    .where(exceptUserId ? and(active, ne(u.id, exceptUserId)) : active);
  return row?.n ?? 0;
}
