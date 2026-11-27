/**
 * Button values kept on the server for providers whose buttons carry too little (Telegram's callback_data is 64 bytes):
 * the button carries "cb:<id>" and the value stays here. Values hold no secrets (a thread id, a request id, a decision),
 * and an id only works for the session's owner anyway (service.ts checks who pressed).
 */
import { eq, lt } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { newId } from "../lib/ids.ts";

export const BUTTON_PREFIX = "cb:";
const RETENTION_MS = 30 * 86_400_000;

let lastPrune = 0;

export async function storeButton(value: string): Promise<string> {
  const db = getDb();
  const now = new Date();
  if (now.getTime() - lastPrune > 3_600_000) {
    lastPrune = now.getTime();
    await db.delete(schema.chatButton).where(lt(schema.chatButton.createdAt, new Date(now.getTime() - RETENTION_MS))).catch(() => {});
  }
  const id = newId("b", 12);
  await db.insert(schema.chatButton).values({ id, value, createdAt: now });
  return `${BUTTON_PREFIX}${id}`;
}

/** The stored value of a "cb:<id>" button, or the value itself when it is not one. */
export async function buttonValue(v: string): Promise<string> {
  if (!v.startsWith(BUTTON_PREFIX)) return v;
  const row = await getDb().query.chatButton.findFirst({ where: eq(schema.chatButton.id, v.slice(BUTTON_PREFIX.length)) });
  return row?.value ?? "";
}
