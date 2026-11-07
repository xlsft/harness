/**
 * The work of boards against external trackers: inputs that pull issues in once, outputs applied to linked issues, and
 * looking at linked issues again. Work runs where the credential lives: admin connections here on a tick, device connections on
 * a running client through ClaimDeviceWork and ReportDeviceWork. docs/design/tracker.md.
 *
 * Claims are single conditional UPDATEs with a lease, so several replicas never do the same work and a crashed worker's lease
 * simply runs out.
 */
import { and, asc, eq, inArray, isNull, lt, lte, or, sql, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { connectionsById, dropConnectionUses, openProvider, providerMessage, type ConnectionRow } from "../tracker/connections.ts";
import { TrackerError, trackerErrorKind } from "../tracker/provider.ts";
import { noteCardActivity, onOutputsPending, visibleBoard, type CardOutputRow, type FieldSet, type InputRow } from "./boards.ts";
import type { Principal } from "./devices.ts";
import { nextCardNumber } from "./projects.ts";

const TICK_MS = 30_000;
const LEASE_MS = 2 * 60_000;
/** Retries of a transient failure after the first attempt; after these the user decides. */
const RETRY_BACKOFF_MS = [60_000, 5 * 60_000, 15 * 60_000];
export const INPUT_MAX_ISSUES = 200;
const CHECK_BATCH = 50;
const CHECK_EVERY_MS = 5 * 60_000;
const POSITION_GAP = 1024;

export interface PulledIssue {
  id: string;
  title: string;
  url: string;
}

function inputDue(now: Date, scope: "admin" | "device"): SQL {
  const i = schema.boardStageInput;
  return and(
    eq(i.connectionScope, scope),
    or(isNull(i.leaseUntil), lt(i.leaseUntil, now)),
    or(isNull(i.lastPolledAt), sql`${i.lastPolledAt} + make_interval(secs => ${i.intervalSeconds}) <= ${now.toISOString()}::timestamptz`),
  )!;
}

function outputDue(now: Date, scope: "admin" | "device"): SQL {
  const co = schema.boardCardOutput;
  return and(eq(co.connectionScope, scope), or(and(eq(co.status, "pending"), lte(co.nextAttemptAt, now)), and(eq(co.status, "running"), lt(co.leaseUntil, now))))!;
}

/**
 * New issues become cards at the end of the stage, once: the ledger row and the card go in one transaction. An issue already
 * seen by this board only refreshes the title of its card, unless someone edited that title.
 */
export async function applyInputResult(input: InputRow, issues: PulledIssue[]): Promise<number> {
  const s = schema.boardStage;
  const c = schema.boardCard;
  const seen = schema.boardInputSeen;
  let created = 0;
  await getDb().transaction(async (tx) => {
    const stage = (await tx.select().from(s).where(eq(s.id, input.stageId)))[0];
    if (!stage) return;
    const board = (await tx.select().from(schema.board).where(eq(schema.board.id, stage.boardId)))[0]!;
    const [{ max } = { max: 0 }] = await tx
      .select({ max: sql<number | null>`max(${c.position})` })
      .from(c)
      .where(eq(c.stageId, stage.id));
    let position = Number(max ?? 0);
    const now = new Date();
    for (const issue of issues.slice(0, INPUT_MAX_ISSUES)) {
      if (!issue.id) continue;
      const fresh = await tx
        .insert(seen)
        .values({ boardId: stage.boardId, connectionScope: input.connectionScope, connectionId: input.connectionId, externalId: issue.id, firstSeenAt: now })
        .onConflictDoNothing()
        .returning({ id: seen.externalId });
      if (!fresh.length) {
        await tx
          .update(c)
          .set({ title: issue.title.slice(0, 500), externalUrl: issue.url, updatedAt: now })
          .where(
            and(
              eq(c.boardId, stage.boardId),
              eq(c.connectionScope, input.connectionScope),
              eq(c.connectionId, input.connectionId),
              eq(c.externalId, issue.id),
              eq(c.titleEdited, false),
              sql`${c.title} <> ${issue.title.slice(0, 500)}`,
              issue.title.trim() ? undefined : sql`false`,
            ),
          );
        continue;
      }
      position += POSITION_GAP;
      const id = newId("crd");
      await tx.insert(c).values({
        id,
        boardId: stage.boardId,
        projectId: board.projectId,
        number: await nextCardNumber(tx, board.projectId),
        stageId: stage.id,
        position,
        title: (issue.title.trim() || issue.id).slice(0, 500),
        connectionScope: input.connectionScope,
        connectionId: input.connectionId,
        externalId: issue.id,
        externalUrl: issue.url,
        externalState: "linked",
        externalCheckedAt: now,
        createdAt: now,
        updatedAt: now,
      });
      await noteCardActivity(tx, id, "", "created", `Pulled in from ${issue.id}`);
      created++;
    }
  });
  return created;
}

async function finishInput(id: string, error: string): Promise<void> {
  const i = schema.boardStageInput;
  const now = new Date();
  await getDb()
    .update(i)
    .set({ lastPolledAt: now, lastError: error, leaseUntil: null, ...(error ? {} : { lastOkAt: now }) })
    .where(eq(i.id, id));
}

/** Failure accounting of one application: transient errors retry with backoff and stay visible, the rest wait for the user. */
export async function finishOutput(row: Pick<CardOutputRow, "id" | "attempts" | "cardId">, error: string, transient: boolean): Promise<void> {
  const co = schema.boardCardOutput;
  const now = new Date();
  const attempts = row.attempts + 1;
  if (!error) {
    await getDb().update(co).set({ status: "done", attempts, lastError: "", leaseUntil: null, updatedAt: now }).where(eq(co.id, row.id));
    await noteCardActivity(getDb(), row.cardId, "", "output", "Applied the stage's output to the linked issue");
    return;
  }
  const retry = transient && attempts <= RETRY_BACKOFF_MS.length;
  if (!retry) await noteCardActivity(getDb(), row.cardId, "", "output", `The stage's output failed: ${error.slice(0, 500)}`);
  await getDb()
    .update(co)
    .set({ status: retry ? "pending" : "failed", attempts, lastError: error.slice(0, 2000), leaseUntil: null, nextAttemptAt: new Date(now.getTime() + (retry ? RETRY_BACKOFF_MS[attempts - 1]! : 0)), updatedAt: now })
    .where(eq(co.id, row.id));
}

export async function applyCheck(cardId: string, result: { missing: boolean; title?: string; url?: string }): Promise<void> {
  const c = schema.boardCard;
  const row = await getDb().query.boardCard.findFirst({ where: eq(c.id, cardId) });
  if (!row || row.externalState === "disconnected" || !row.connectionId) return;
  const state = result.missing ? "missing" : "linked";
  const title = !result.missing && !row.titleEdited && result.title?.trim() ? result.title.slice(0, 500) : row.title;
  const url = !result.missing && result.url ? result.url : row.externalUrl;
  const now = new Date();
  await getDb()
    .update(c)
    .set({ externalState: state, title, externalUrl: url, externalCheckedAt: now, ...(state !== row.externalState || title !== row.title ? { updatedAt: now } : {}) })
    .where(eq(c.id, cardId));
}

// ---- admin connections, on the server ----

async function claimAdminInputs(now: Date, boardId?: string): Promise<InputRow[]> {
  const i = schema.boardStageInput;
  const s = schema.boardStage;
  const where = boardId ? and(eq(i.connectionScope, "admin"), or(isNull(i.leaseUntil), lt(i.leaseUntil, now)), inArray(i.stageId, getDb().select({ id: s.id }).from(s).where(eq(s.boardId, boardId)))) : inputDue(now, "admin");
  return getDb()
    .update(i)
    .set({ leaseUntil: new Date(now.getTime() + LEASE_MS) })
    .where(where)
    .returning();
}

async function runAdminInputs(inputs: InputRow[]): Promise<number> {
  if (!inputs.length) return 0;
  const conns = await connectionsById([...new Set(inputs.map((x) => x.connectionId))]);
  let created = 0;
  for (const input of inputs) {
    const conn = conns.get(input.connectionId);
    if (!conn) {
      await finishInput(input.id, "The connection no longer exists");
      continue;
    }
    if (!conn.enabled) {
      await finishInput(input.id, `${conn.name} is disabled`);
      continue;
    }
    try {
      const { issues } = await openProvider(conn).search(input.query, { limit: INPUT_MAX_ISSUES });
      created += await applyInputResult(input, issues);
      await finishInput(input.id, "");
    } catch (err) {
      await finishInput(input.id, providerMessage(err));
    }
  }
  return created;
}

async function applyOutput(conn: ConnectionRow | undefined, externalId: string, row: CardOutputRow): Promise<void> {
  if (!conn) return finishOutput(row, "The connection no longer exists", false);
  if (!conn.enabled) return finishOutput(row, `${conn.name} is disabled`, false);
  if (!externalId) return finishOutput(row, "The card is no longer linked to an issue", false);
  try {
    const provider = openProvider(conn);
    const fields = (row.fields ?? []) as FieldSet[];
    if (fields.length) await provider.update(externalId, { fields });
    if (row.comment) await provider.comment(externalId, row.comment);
    await finishOutput(row, "", false);
  } catch (err) {
    await finishOutput(row, providerMessage(err), trackerErrorKind(err) === "transient");
  }
}

async function runAdminOutputs(now: Date): Promise<void> {
  const co = schema.boardCardOutput;
  const c = schema.boardCard;
  const claimed = await getDb()
    .update(co)
    .set({ status: "running", leaseUntil: new Date(now.getTime() + LEASE_MS), updatedAt: now })
    .where(outputDue(now, "admin"))
    .returning();
  if (!claimed.length) return;
  const conns = await connectionsById([...new Set(claimed.map((x) => x.connectionId))]);
  const cards = await getDb()
    .select({ id: c.id, externalId: c.externalId, connectionId: c.connectionId })
    .from(c)
    .where(inArray(c.id, claimed.map((x) => x.cardId)));
  for (const row of claimed) {
    const card = cards.find((x) => x.id === row.cardId);
    await applyOutput(conns.get(row.connectionId), card && card.connectionId === row.connectionId ? card.externalId : "", row);
  }
}

async function runAdminChecks(now: Date): Promise<void> {
  const c = schema.boardCard;
  const rows = await getDb()
    .select({ id: c.id, connectionId: c.connectionId, externalId: c.externalId })
    .from(c)
    .where(and(eq(c.connectionScope, "admin"), inArray(c.externalState, ["linked", "missing"]), or(isNull(c.externalCheckedAt), lt(c.externalCheckedAt, new Date(now.getTime() - CHECK_EVERY_MS)))))
    .orderBy(sql`${c.externalCheckedAt} asc nulls first`)
    .limit(CHECK_BATCH);
  if (!rows.length) return;
  const conns = await connectionsById([...new Set(rows.map((x) => x.connectionId))]);
  for (const row of rows) {
    const conn = conns.get(row.connectionId);
    if (!conn?.enabled) continue;
    try {
      const issue = await openProvider(conn).get(row.externalId);
      await applyCheck(row.id, { missing: false, title: issue.title, url: issue.url });
    } catch (err) {
      // Only "it is not there" says anything about the issue; an auth or network failure says something about the connection.
      if (err instanceof TrackerError && err.kind === "not_found") await applyCheck(row.id, { missing: true });
    }
  }
}

let running = false;

/** One pass over what is due. Exported for tests and for the Sync button of a board. */
export async function runBoardSyncOnce(): Promise<number> {
  if (running) return 0;
  running = true;
  try {
    await ensureDb();
    const now = new Date();
    const created = await runAdminInputs(await claimAdminInputs(now));
    await runAdminOutputs(new Date());
    await runAdminChecks(new Date());
    return created;
  } finally {
    running = false;
  }
}

export function startBoardSync(): void {
  if (process.env.HARNESS_BOARD_SYNC === "off") return;
  const tick = () => void runBoardSyncOnce().catch((err: unknown) => console.error("[harness] board sync failed", (err as Error).message));
  setInterval(tick, TICK_MS).unref();
  onOutputsPending(() => setTimeout(tick, 0).unref());
}

/** A move into a stage with an output works without the tick when something else starts the server's sync, as tests do. */
export function wakeOutputsOnMove(): void {
  onOutputsPending(() => {
    setTimeout(() => void runBoardSyncOnce().catch(() => {}), 0).unref();
  });
}

/** The Sync button: admin inputs now, whatever their schedule; device inputs on the next claim. */
export async function syncBoard(p: Principal, id: string): Promise<number> {
  const board = await visibleBoard(p, id);
  const i = schema.boardStageInput;
  const s = schema.boardStage;
  const now = new Date();
  await getDb()
    .update(i)
    .set({ lastPolledAt: null })
    .where(and(eq(i.connectionScope, "device"), inArray(i.stageId, getDb().select({ id: s.id }).from(s).where(eq(s.boardId, board.id)))));
  return runAdminInputs(await claimAdminInputs(now, board.id));
}

// ---- device connections, through a running client ----

/** What is on the caller's personal boards on these connections, as a condition on a stage id. */
function ownStages(p: Principal) {
  const s = schema.boardStage;
  const b = schema.board;
  return getDb()
    .select({ id: s.id })
    .from(s)
    .innerJoin(b, eq(b.id, s.boardId))
    .where(and(eq(b.scope, "personal"), eq(b.ownerId, p.user.id)));
}

export async function claimDeviceWork(p: Principal, connectionIds: string[]) {
  await ensureDb();
  const empty = { inputs: [], outputs: [], checks: [] };
  const ids = [...new Set(connectionIds)].filter(Boolean).slice(0, 100);
  if (!ids.length) return empty;
  const db = getDb();
  const now = new Date();
  const lease = new Date(now.getTime() + LEASE_MS);
  const i = schema.boardStageInput;
  const inputs = await db
    .update(i)
    .set({ leaseUntil: lease })
    .where(and(inputDue(now, "device"), inArray(i.connectionId, ids), inArray(i.stageId, ownStages(p))))
    .returning();
  const co = schema.boardCardOutput;
  const c = schema.boardCard;
  const b = schema.board;
  const ownCards = db
    .select({ id: c.id })
    .from(c)
    .innerJoin(b, eq(b.id, c.boardId))
    .where(and(eq(b.scope, "personal"), eq(b.ownerId, p.user.id)));
  const outputs = await db
    .update(co)
    .set({ status: "running", leaseUntil: lease, updatedAt: now })
    .where(and(outputDue(now, "device"), inArray(co.connectionId, ids), inArray(co.cardId, ownCards)))
    .returning();
  const outCards = outputs.length
    ? await db
        .select({ id: c.id, externalId: c.externalId })
        .from(c)
        .where(inArray(c.id, outputs.map((x) => x.cardId)))
    : [];
  const due = await db
    .select({ id: c.id })
    .from(c)
    .innerJoin(b, eq(b.id, c.boardId))
    .where(
      and(
        eq(b.scope, "personal"),
        eq(b.ownerId, p.user.id),
        eq(c.connectionScope, "device"),
        inArray(c.connectionId, ids),
        inArray(c.externalState, ["linked", "missing"]),
        or(isNull(c.externalCheckedAt), lt(c.externalCheckedAt, new Date(now.getTime() - CHECK_EVERY_MS))),
      ),
    )
    .orderBy(asc(c.externalCheckedAt))
    .limit(CHECK_BATCH);
  const checks = due.length
    ? await db
        .update(c)
        .set({ externalCheckedAt: now })
        .where(inArray(c.id, due.map((x) => x.id)))
        .returning({ id: c.id, connectionId: c.connectionId, externalId: c.externalId })
    : [];
  return {
    inputs: inputs.map((x) => ({ inputId: x.id, connectionId: x.connectionId, query: x.query })),
    outputs: outputs.map((x) => ({
      outputId: x.id,
      connectionId: x.connectionId,
      externalId: outCards.find((k) => k.id === x.cardId)?.externalId ?? "",
      fields: (x.fields ?? []) as FieldSet[],
      comment: x.comment,
    })),
    checks: checks.map((x) => ({ cardId: x.id, connectionId: x.connectionId, externalId: x.externalId })),
  };
}

export async function reportDeviceWork(
  p: Principal,
  report: {
    inputs: { inputId: string; error: string; issues: PulledIssue[] }[];
    outputs: { outputId: string; error: string; transient: boolean }[];
    checks: { cardId: string; missing: boolean; title: string; url: string }[];
  },
): Promise<number> {
  await ensureDb();
  const db = getDb();
  const i = schema.boardStageInput;
  const co = schema.boardCardOutput;
  const c = schema.boardCard;
  const b = schema.board;
  let created = 0;
  if (report.inputs.length) {
    const rows = await db
      .select()
      .from(i)
      .where(and(inArray(i.id, report.inputs.map((x) => x.inputId)), eq(i.connectionScope, "device"), inArray(i.stageId, ownStages(p))));
    for (const row of rows) {
      const r = report.inputs.find((x) => x.inputId === row.id)!;
      if (!r.error) created += await applyInputResult(row, r.issues);
      await finishInput(row.id, r.error.slice(0, 2000));
    }
  }
  const own = (ids: string[]) =>
    db
      .select({ id: c.id })
      .from(c)
      .innerJoin(b, eq(b.id, c.boardId))
      .where(and(inArray(c.id, ids), eq(b.scope, "personal"), eq(b.ownerId, p.user.id)));
  if (report.outputs.length) {
    const rows = await db
      .select()
      .from(co)
      .where(and(inArray(co.id, report.outputs.map((x) => x.outputId)), eq(co.connectionScope, "device"), eq(co.status, "running")));
    const mine = rows.length ? new Set((await own(rows.map((r) => r.cardId))).map((r) => r.id)) : new Set<string>();
    for (const row of rows) {
      if (!mine.has(row.cardId)) continue;
      const r = report.outputs.find((x) => x.outputId === row.id)!;
      await finishOutput(row, r.error, r.transient);
    }
  }
  if (report.checks.length) {
    const mine = new Set((await own(report.checks.map((x) => x.cardId))).map((r) => r.id));
    for (const r of report.checks) if (mine.has(r.cardId)) await applyCheck(r.cardId, r);
  }
  return created;
}

export async function forgetDeviceConnection(p: Principal, connectionId: string): Promise<void> {
  if (!connectionId) throw new ApiError("invalid_request", "connection_id is required");
  await ensureDb();
  const b = schema.board;
  const boards = await getDb()
    .select({ id: b.id })
    .from(b)
    .where(and(eq(b.scope, "personal"), eq(b.ownerId, p.user.id)));
  if (boards.length)
    await dropConnectionUses(
      "device",
      connectionId,
      boards.map((r) => r.id),
    );
}
