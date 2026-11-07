/**
 * Internal boards of a project: ordered stages with optional inputs and outputs per connection, and cards (what the Issues screen
 * calls issues) optionally linked to an external issue. The rules are in docs/design/tracker.md; this file and board-sync.ts
 * implement them.
 *
 * Every read goes through `visible` or `visibleBoardIds`: what the caller holds no level on does not exist for them, whatever
 * their role, and a personal board of someone else neither.
 */
import { and, asc, desc, eq, ilike, inArray, lte, or, sql, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { connectionsById } from "../tracker/connections.ts";
import type { Principal } from "./devices.ts";
import { atLeast, findProject, levels, nextCardNumber, tickerOf, type Level, type ProjectRow } from "./projects.ts";

export type BoardRow = typeof schema.board.$inferSelect;
export type StageRow = typeof schema.boardStage.$inferSelect;
export type InputRow = typeof schema.boardStageInput.$inferSelect;
export type OutputRow = typeof schema.boardStageOutput.$inferSelect;
export type CardRow = typeof schema.boardCard.$inferSelect;
export type CardOutputRow = typeof schema.boardCardOutput.$inferSelect;
export type ConnScope = "admin" | "device";

export interface FieldSet {
  name: string;
  value: string;
}

export interface StageView {
  row: StageRow;
  inputs: InputRow[];
  outputs: OutputRow[];
}

export interface BoardView {
  row: BoardRow;
  stages: StageView[];
  canConfigure: boolean;
  canEdit: boolean;
}

export interface RelationView {
  kind: "blocks" | "blocked_by" | "relates" | "duplicates";
  cardId: string;
  key: string;
  title: string;
}

export interface CardView {
  row: CardRow;
  key: string;
  output?: CardOutputRow;
  relations: RelationView[];
}

export const PRIORITIES = ["low", "normal", "high", "urgent"] as const;
export type PriorityName = (typeof PRIORITIES)[number];

export interface CardFilter {
  projectId?: string;
  boardId?: string;
  stageId?: string;
  query?: string;
  assigneeIds?: string[];
  unassigned?: boolean;
  labels?: string[];
  priorities?: PriorityName[];
  dueBefore?: string;
  sessionId?: string;
  blocked?: boolean;
  sort?: string;
}

export interface StageInputSpec {
  id?: string;
  connection: { scope: ConnScope; id: string };
  query: string;
  intervalSeconds?: number;
}

export interface StageOutputSpec {
  id?: string;
  connection: { scope: ConnScope; id: string };
  fields: FieldSet[];
  comment?: string;
}

export interface StageSpec {
  id?: string;
  name: string;
  inputs?: StageInputSpec[];
  outputs?: StageOutputSpec[];
}

const NAME_MAX = 200;
const TITLE_MAX = 500;
const DESCRIPTION_MAX = 64 << 10;
const QUERY_MAX = 2000;
const COMMENT_MAX = 8 << 10;
const MAX_STAGES = 30;
const MAX_PER_STAGE = 8;
const MAX_LINKS = 100;
const DEFAULT_STAGES = ["To do", "In progress", "Done"];
export const INTERVAL_DEFAULT = 300;
export const INTERVAL_MIN = 60;
export const INTERVAL_MAX = 86_400;
const POSITION_GAP = 1024;

type Tx = Parameters<Parameters<ReturnType<typeof getDb>["transaction"]>[0]>[0];

function checkName(name: string, what: string): string {
  const n = name.trim();
  if (!n) throw new ApiError("invalid_request", `${what} needs a name`);
  if (n.length > NAME_MAX) throw new ApiError("invalid_request", `Names have at most ${NAME_MAX} characters`);
  return n;
}

function checkTitle(title: string): string {
  const t = title.trim();
  if (!t) throw new ApiError("invalid_request", "title is required");
  if (t.length > TITLE_MAX) throw new ApiError("invalid_request", `Titles have at most ${TITLE_MAX} characters`);
  return t;
}

function checkDescription(text: string): string {
  if (text.length > DESCRIPTION_MAX) throw new ApiError("invalid_request", `Descriptions have at most ${DESCRIPTION_MAX} characters`);
  return text;
}

/** A board shows when its project does, and a personal one only to its owner. */
function visibleWhere(p: Principal, lv: Map<string, Level>): SQL {
  const b = schema.board;
  if (!lv.size) return sql`false`;
  return and(inArray(b.projectId, [...lv.keys()]), or(eq(b.scope, "shared"), and(eq(b.scope, "personal"), eq(b.ownerId, p.user.id))))!;
}

function canConfigure(p: Principal, b: BoardRow, level: Level | undefined): boolean {
  return b.scope === "personal" ? b.ownerId === p.user.id : atLeast(level, "manage");
}

function canWork(p: Principal, b: BoardRow, level: Level | undefined): boolean {
  return b.scope === "personal" ? b.ownerId === p.user.id : atLeast(level, "edit");
}

async function visible(p: Principal, id: string): Promise<BoardRow> {
  if (!id) throw new ApiError("invalid_request", "A board id is required");
  const lv = await levels(p);
  const row = await getDb().query.board.findFirst({ where: and(eq(schema.board.id, id), visibleWhere(p, lv)) });
  if (!row) throw new ApiError("not_found", `Board ${id} not found`);
  return row;
}

async function configurable(p: Principal, id: string): Promise<BoardRow> {
  const row = await visible(p, id);
  if (!canConfigure(p, row, (await levels(p)).get(row.projectId))) throw new ApiError("permission", "You need manage on this project to change its shared boards");
  return row;
}

const KEY = /^[A-Za-z][A-Za-z0-9]*-[0-9]+$/;

/** A card the caller can see, by id, by key such as ABC-12, or by a linked issue's own id. */
async function cardOf(p: Principal, idOrKey: string): Promise<{ card: CardRow; board: BoardRow; level: Level | undefined }> {
  if (!idOrKey) throw new ApiError("invalid_request", "A card id is required");
  const lv = await levels(p);
  const c = schema.boardCard;
  const b = schema.board;
  const conds: SQL[] = [eq(c.id, idOrKey)];
  if (KEY.test(idOrKey)) {
    const [ticker, number] = idOrKey.toUpperCase().split("-") as [string, string];
    const pr = schema.project;
    const projects = lv.size ? await getDb().select().from(pr).where(inArray(pr.id, [...lv.keys()])) : [];
    const ids = projects.filter((x) => tickerOf(x) === ticker).map((x) => x.id);
    if (ids.length) conds.push(and(inArray(c.projectId, ids), eq(c.number, Number(number)))!);
    conds.push(eq(c.externalId, idOrKey));
  }
  const rows = await getDb()
    .select({ card: c, board: b })
    .from(c)
    .innerJoin(b, eq(b.id, c.boardId))
    .where(and(or(...conds), visibleWhere(p, lv)))
    .limit(2);
  const row = rows[0];
  if (!row) throw new ApiError("not_found", `Card ${idOrKey} not found`);
  return { ...row, level: lv.get(row.card.projectId) };
}

async function workable(p: Principal, idOrKey: string): Promise<CardRow & { _board: BoardRow }> {
  const { card, board, level } = await cardOf(p, idOrKey);
  if (!canWork(p, board, level)) throw new ApiError("permission", "You need edit on this project to change its issues");
  return { ...card, _board: board };
}

async function checkStages(scope: string, specs: StageSpec[]): Promise<void> {
  if (!specs.length) throw new ApiError("invalid_request", "A board has at least one stage");
  if (specs.length > MAX_STAGES) throw new ApiError("invalid_request", `A board has at most ${MAX_STAGES} stages`);
  const adminIds = new Set<string>();
  for (const s of specs) {
    checkName(s.name, "A stage");
    for (const list of [s.inputs ?? [], s.outputs ?? []]) {
      if (list.length > MAX_PER_STAGE) throw new ApiError("invalid_request", `A stage has at most ${MAX_PER_STAGE} inputs and ${MAX_PER_STAGE} outputs`);
      const seen = new Set<string>();
      for (const item of list) {
        const { scope: cs, id } = item.connection;
        if (cs !== "admin" && cs !== "device") throw new ApiError("invalid_request", "Choose a connection");
        if (!id || id.length > 100) throw new ApiError("invalid_request", "Choose a connection");
        if (scope === "shared" && cs === "device") throw new ApiError("invalid_request", "A shared board uses company connections only");
        const k = `${cs}:${id}`;
        if (seen.has(k)) throw new ApiError("invalid_request", `Stage ${s.name}: at most one input and one output per connection`);
        seen.add(k);
        if (cs === "admin") adminIds.add(id);
      }
    }
    for (const i of s.inputs ?? []) {
      if (!i.query.trim()) throw new ApiError("invalid_request", `Stage ${s.name}: an input needs a query`);
      if (i.query.length > QUERY_MAX) throw new ApiError("invalid_request", `Queries have at most ${QUERY_MAX} characters`);
    }
    for (const o of s.outputs ?? []) {
      const fields = o.fields.filter((f) => f.name.trim());
      if (!fields.length && !o.comment?.trim()) throw new ApiError("invalid_request", `Stage ${s.name}: an output sets a field or posts a comment`);
      if ((o.comment ?? "").length > COMMENT_MAX) throw new ApiError("invalid_request", `Comments have at most ${COMMENT_MAX} characters`);
    }
  }
  const found = await connectionsById([...adminIds]);
  for (const id of adminIds) if (!found.has(id)) throw new ApiError("invalid_request", `No connection ${id}`);
}

function clampInterval(v: number | undefined): number {
  if (!v) return INTERVAL_DEFAULT;
  return Math.min(Math.max(Math.trunc(v), INTERVAL_MIN), INTERVAL_MAX);
}

/** Writes the stages as given. What keeps its id keeps its state; what is gone goes, and a gone stage's cards go to the first one. */
async function writeStages(tx: Tx, boardId: string, specs: StageSpec[]): Promise<void> {
  const s = schema.boardStage;
  const i = schema.boardStageInput;
  const o = schema.boardStageOutput;
  const c = schema.boardCard;
  const co = schema.boardCardOutput;
  const existing = await tx.select().from(s).where(eq(s.boardId, boardId));
  const known = new Set(existing.map((r) => r.id));
  const ids: string[] = [];
  for (const [position, spec] of specs.entries()) {
    const id = spec.id && known.has(spec.id) ? spec.id : newId("stg");
    ids.push(id);
    const name = checkName(spec.name, "A stage");
    if (known.has(id)) await tx.update(s).set({ name, position }).where(eq(s.id, id));
    else await tx.insert(s).values({ id, boardId, name, position });

    const inputs = await tx.select().from(i).where(eq(i.stageId, id));
    const keepInputs = new Set((spec.inputs ?? []).map((x) => x.id).filter(Boolean));
    const dropInputs = inputs.filter((x) => !keepInputs.has(x.id)).map((x) => x.id);
    if (dropInputs.length) await tx.delete(i).where(inArray(i.id, dropInputs));
    for (const x of spec.inputs ?? []) {
      const same = inputs.find((r) => r.id === x.id);
      const values = { connectionScope: x.connection.scope, connectionId: x.connection.id, query: x.query.trim(), intervalSeconds: clampInterval(x.intervalSeconds) };
      if (same) {
        // A new query is a new door: poll it soon.
        const changed = same.query !== values.query || same.connectionId !== values.connectionId;
        await tx
          .update(i)
          .set({ ...values, ...(changed ? { lastPolledAt: null, lastError: "" } : {}) })
          .where(eq(i.id, same.id));
      } else {
        await tx.insert(i).values({ id: newId("sin"), stageId: id, ...values });
      }
    }

    const outputs = await tx.select().from(o).where(eq(o.stageId, id));
    const keepOutputs = new Set((spec.outputs ?? []).map((x) => x.id).filter(Boolean));
    const dropOutputs = outputs.filter((x) => !keepOutputs.has(x.id)).map((x) => x.id);
    if (dropOutputs.length) await tx.delete(o).where(inArray(o.id, dropOutputs));
    for (const x of spec.outputs ?? []) {
      const values = {
        connectionScope: x.connection.scope,
        connectionId: x.connection.id,
        fields: x.fields.filter((f) => f.name.trim()).map((f) => ({ name: f.name.trim(), value: f.value })),
        comment: x.comment?.trim() ?? "",
      };
      if (outputs.some((r) => r.id === x.id)) await tx.update(o).set(values).where(eq(o.id, x.id!));
      else await tx.insert(o).values({ id: newId("sou"), stageId: id, ...values });
    }
  }
  const gone = existing.filter((r) => !ids.includes(r.id)).map((r) => r.id);
  if (gone.length) {
    const [{ max } = { max: 0 }] = await tx
      .select({ max: sql<number>`coalesce(max(${c.position}), 0)` })
      .from(c)
      .where(eq(c.stageId, ids[0]!));
    await tx.update(c).set({ stageId: ids[0]!, position: sql`${Number(max) + POSITION_GAP}::double precision + ${c.position}`, updatedAt: new Date(), version: sql`${c.version} + 1` }).where(inArray(c.stageId, gone));
    await tx.update(co).set({ status: "superseded", leaseUntil: null, updatedAt: new Date() }).where(and(inArray(co.stageId, gone), inArray(co.status, ["pending", "failed"])));
    await tx.delete(s).where(inArray(s.id, gone));
  }
}

async function loadStages(boardIds: string[]): Promise<Map<string, StageView[]>> {
  const out = new Map<string, StageView[]>();
  if (!boardIds.length) return out;
  const db = getDb();
  const s = schema.boardStage;
  const stages = await db.select().from(s).where(inArray(s.boardId, boardIds)).orderBy(asc(s.position));
  const ids = stages.map((r) => r.id);
  const inputs = ids.length ? await db.select().from(schema.boardStageInput).where(inArray(schema.boardStageInput.stageId, ids)) : [];
  const outputs = ids.length ? await db.select().from(schema.boardStageOutput).where(inArray(schema.boardStageOutput.stageId, ids)) : [];
  for (const row of stages) {
    const list = out.get(row.boardId) ?? [];
    list.push({ row, inputs: inputs.filter((x) => x.stageId === row.id), outputs: outputs.filter((x) => x.stageId === row.id) });
    out.set(row.boardId, list);
  }
  return out;
}

async function view(p: Principal, row: BoardRow): Promise<BoardView> {
  const stages = await loadStages([row.id]);
  const level = (await levels(p)).get(row.projectId);
  return { row, stages: stages.get(row.id) ?? [], canConfigure: canConfigure(p, row, level), canEdit: canWork(p, row, level) };
}

export async function listBoards(p: Principal, projectId?: string): Promise<BoardView[]> {
  const lv = await levels(p);
  const b = schema.board;
  const rows = await getDb()
    .select()
    .from(b)
    .where(and(visibleWhere(p, lv), projectId ? eq(b.projectId, projectId) : undefined))
    .orderBy(asc(b.scope), asc(b.name), asc(b.id));
  const stages = await loadStages(rows.map((r) => r.id));
  return rows.map((row) => {
    const level = lv.get(row.projectId);
    return { row, stages: stages.get(row.id) ?? [], canConfigure: canConfigure(p, row, level), canEdit: canWork(p, row, level) };
  });
}

export async function createBoard(p: Principal, input: { projectId: string; scope: "shared" | "personal"; name: string; stages?: StageSpec[] }): Promise<BoardView> {
  await ensureDb();
  if (input.scope !== "shared" && input.scope !== "personal") throw new ApiError("invalid_request", "A board is shared or personal");
  const { row: project, level } = await findProject(p, input.projectId);
  if (input.scope === "shared" && !atLeast(level, "manage")) throw new ApiError("permission", "You need manage on this project to add a shared board");
  const specs = input.stages?.length ? input.stages : DEFAULT_STAGES.map((name) => ({ name }));
  await checkStages(input.scope, specs);
  const now = new Date();
  const row: BoardRow = {
    id: newId("brd"),
    projectId: project.id,
    scope: input.scope,
    ownerId: input.scope === "personal" ? p.user.id : null,
    name: checkName(input.name, "A board"),
    createdAt: now,
    updatedAt: now,
  };
  await getDb().transaction(async (tx) => {
    await tx.insert(schema.board).values(row);
    await writeStages(tx, row.id, specs);
  });
  return view(p, row);
}

export async function getBoard(p: Principal, id: string): Promise<{ board: BoardView; cards: CardView[] }> {
  const row = await visible(p, id);
  const c = schema.boardCard;
  const cards = await getDb().select().from(c).where(eq(c.boardId, id)).orderBy(asc(c.position)).limit(2000);
  return { board: await view(p, row), cards: await enrich(p, cards) };
}

export async function updateBoard(p: Principal, id: string, input: { name?: string; stages?: StageSpec[] }): Promise<BoardView> {
  const row = await configurable(p, id);
  if (input.stages?.length) await checkStages(row.scope, input.stages);
  const b = schema.board;
  await getDb().transaction(async (tx) => {
    await tx
      .update(b)
      .set({ name: input.name ? checkName(input.name, "A board") : row.name, updatedAt: new Date() })
      .where(eq(b.id, id));
    if (input.stages?.length) await writeStages(tx, id, input.stages);
  });
  return view(p, (await getDb().query.board.findFirst({ where: eq(b.id, id) }))!);
}

export async function deleteBoard(p: Principal, id: string): Promise<void> {
  await configurable(p, id);
  await getDb().transaction(async (tx) => {
    await tx.delete(schema.boardCard).where(eq(schema.boardCard.boardId, id));
    await tx.delete(schema.board).where(eq(schema.board.id, id));
  });
}

const keyOf = (card: Pick<CardRow, "externalId" | "number">, ticker: string) => card.externalId || `${ticker}-${card.number}`;

async function tickers(projectIds: string[]): Promise<Map<string, string>> {
  if (!projectIds.length) return new Map();
  const pr = schema.project;
  const rows: ProjectRow[] = await getDb().select().from(pr).where(inArray(pr.id, [...new Set(projectIds)]));
  return new Map(rows.map((r) => [r.id, tickerOf(r)]));
}

/** Keys, the latest output, and the relations to cards this caller can see: a relation does not reveal what it may not. */
async function enrich(p: Principal, cards: CardRow[]): Promise<CardView[]> {
  if (!cards.length) return [];
  const db = getDb();
  const ids = cards.map((c) => c.id);
  const co = schema.boardCardOutput;
  const rows = await db
    .select()
    .from(co)
    .where(and(inArray(co.cardId, ids), inArray(co.status, ["pending", "running", "failed", "done"])))
    .orderBy(desc(co.createdAt));
  const latest = new Map<string, CardOutputRow>();
  for (const r of rows) if (!latest.has(r.cardId)) latest.set(r.cardId, r);

  const rel = schema.boardCardRelation;
  const rels = await db.select().from(rel).where(or(inArray(rel.cardId, ids), inArray(rel.otherId, ids)));
  const otherIds = [...new Set(rels.flatMap((r) => [r.cardId, r.otherId]))];
  const lv = await levels(p);
  const c = schema.boardCard;
  const b = schema.board;
  const others = otherIds.length
    ? (
        await db
          .select({ card: c })
          .from(c)
          .innerJoin(b, eq(b.id, c.boardId))
          .where(and(inArray(c.id, otherIds), visibleWhere(p, lv)))
      ).map((r) => r.card)
    : [];
  const names = await tickers([...cards, ...others].map((x) => x.projectId));
  const seen = new Map(others.map((o) => [o.id, o]));
  const ref = (id: string) => {
    const o = seen.get(id);
    return o ? { cardId: id, key: keyOf(o, names.get(o.projectId) ?? ""), title: o.title } : undefined;
  };
  return cards.map((row) => {
    const relations: RelationView[] = [];
    for (const r of rels) {
      if (r.cardId === row.id) {
        const o = ref(r.otherId);
        if (o) relations.push({ kind: r.kind as RelationView["kind"], ...o });
      } else if (r.otherId === row.id) {
        const o = ref(r.cardId);
        if (o) relations.push({ kind: r.kind === "blocks" ? "blocked_by" : (r.kind as RelationView["kind"]), ...o });
      }
    }
    return { row, key: keyOf(row, names.get(row.projectId) ?? ""), output: latest.get(row.id), relations };
  });
}

export async function cardView(p: Principal, id: string): Promise<CardView> {
  const row = await getDb().query.boardCard.findFirst({ where: eq(schema.boardCard.id, id) });
  if (!row) throw new ApiError("not_found", `Card ${id} not found`);
  return (await enrich(p, [row]))[0]!;
}

export async function listCards(p: Principal, f: CardFilter, limitIn?: number): Promise<CardView[]> {
  const lv = await levels(p);
  const c = schema.boardCard;
  const conds: (SQL | undefined)[] = [visibleWhere(p, lv)];
  if (f.projectId) conds.push(eq(c.projectId, f.projectId));
  if (f.boardId) conds.push(eq(c.boardId, f.boardId));
  if (f.stageId) conds.push(eq(c.stageId, f.stageId));
  if (f.sessionId) conds.push(sql`${c.sessionIds} ? ${f.sessionId}`);
  if (f.unassigned) conds.push(sql`jsonb_array_length(${c.assigneeIds}) = 0`);
  for (const a of f.assigneeIds ?? []) conds.push(sql`${c.assigneeIds} ? ${a === "me" ? p.user.id : a}`);
  for (const l of f.labels ?? []) conds.push(sql`${c.labels} ? ${l}`);
  if (f.priorities?.length) conds.push(inArray(c.priority, f.priorities));
  if (f.dueBefore) conds.push(lte(c.dueAt, parseDate(f.dueBefore)));
  if (f.blocked) {
    conds.push(sql`exists (select 1 from ${schema.boardCardRelation} r where r.other_id = ${c.id} and r.kind = 'blocks')`);
  }
  const q = f.query?.trim();
  if (q) {
    const pat = `%${q.replace(/[\\%_]/g, "\\$&")}%`;
    const keyMatch = KEY.test(q) ? await keyCondition(lv, q) : undefined;
    conds.push(or(ilike(c.title, pat), ilike(c.description, pat), ilike(c.externalId, pat), keyMatch));
  }
  const limit = Math.min(Math.max(limitIn || 200, 1), 500);
  const order =
    f.sort === "created"
      ? [desc(c.createdAt)]
      : f.sort === "priority"
        ? [sql`case ${c.priority} when 'urgent' then 0 when 'high' then 1 when 'normal' then 2 else 3 end`, desc(c.updatedAt)]
        : f.sort === "due"
          ? [sql`${c.dueAt} asc nulls last`, desc(c.updatedAt)]
          : [desc(c.updatedAt)];
  const rows = await getDb()
    .select({ card: c })
    .from(c)
    .innerJoin(schema.board, eq(schema.board.id, c.boardId))
    .where(and(...conds))
    .orderBy(...order)
    .limit(limit);
  return enrich(p, rows.map((r) => r.card));
}

async function keyCondition(lv: Map<string, Level>, q: string): Promise<SQL | undefined> {
  const [ticker, number] = q.toUpperCase().split("-") as [string, string];
  const pr = schema.project;
  const c = schema.boardCard;
  const rows = lv.size ? await getDb().select().from(pr).where(inArray(pr.id, [...lv.keys()])) : [];
  const ids = rows.filter((x) => tickerOf(x) === ticker).map((x) => x.id);
  return ids.length ? and(inArray(c.projectId, ids), eq(c.number, Number(number))) : undefined;
}

export async function getCard(p: Principal, id: string): Promise<CardView> {
  return (await enrich(p, [(await cardOf(p, id)).card]))[0]!;
}

function parseDate(v: string): Date {
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) throw new ApiError("invalid_request", "Dates are RFC 3339");
  return d;
}

function checkLabels(labels: string[]): string[] {
  const list = [...new Set(labels.map((l) => l.trim()).filter(Boolean))];
  if (list.length > 30) throw new ApiError("invalid_request", "A card has at most 30 labels");
  if (list.some((l) => l.length > 60)) throw new ApiError("invalid_request", "Labels have at most 60 characters");
  return list;
}

function checkAssignees(p: Principal, ids: string[]): string[] {
  const list = [...new Set(ids.map((a) => (a === "me" ? p.user.id : a)).filter(Boolean))];
  if (list.length > 20) throw new ApiError("invalid_request", "A card has at most 20 assignees");
  return list;
}

/** Assigning needs someone who can see the card: a personal board has only its owner, a shared one those with a level. */
async function checkAssignable(board: BoardRow, ids: string[]): Promise<void> {
  if (!ids.length) return;
  if (board.scope === "personal") {
    if (ids.some((a) => a !== board.ownerId)) throw new ApiError("invalid_request", "Only you can be assigned on your personal board");
    return;
  }
  const u = schema.user;
  const found = new Set((await getDb().select({ id: u.id }).from(u).where(inArray(u.id, ids))).map((r) => r.id));
  for (const a of ids) if (!found.has(a)) throw new ApiError("invalid_request", `No user ${a}`);
}

async function note(db: Pick<Tx, "insert">, cardId: string, actorId: string, kind: string, summary: string): Promise<void> {
  await db.insert(schema.boardCardActivity).values({ id: newId("act"), cardId, actorId, kind, summary: summary.slice(0, 1000), createdAt: new Date() });
}

export { note as noteCardActivity };

function addTo(list: unknown, value: string): string[] {
  const ids = Array.isArray(list) ? (list as string[]) : [];
  if (!value || ids.includes(value)) return ids;
  return [...ids, value].slice(-MAX_LINKS);
}

async function stageOfBoard(boardId: string, stageId?: string): Promise<StageRow> {
  const s = schema.boardStage;
  const row = stageId ? await getDb().query.boardStage.findFirst({ where: and(eq(s.id, stageId), eq(s.boardId, boardId)) }) : (await getDb().select().from(s).where(eq(s.boardId, boardId)).orderBy(asc(s.position)).limit(1))[0];
  if (!row) throw new ApiError("invalid_request", stageId ? `This board has no stage ${stageId}` : "This board has no stages");
  return row;
}

async function endPosition(db: Pick<Tx, "select">, stageId: string): Promise<number> {
  const c = schema.boardCard;
  const [r] = await db
    .select({ max: sql<number | null>`max(${c.position})` })
    .from(c)
    .where(eq(c.stageId, stageId));
  return Number(r?.max ?? 0) + POSITION_GAP;
}

export async function createCard(
  p: Principal,
  input: { boardId: string; stageId?: string; title: string; description?: string; sessionId?: string; priority?: PriorityName; labels?: string[]; assigneeIds?: string[]; dueAt?: string },
): Promise<CardView> {
  const board = await visible(p, input.boardId);
  if (!canWork(p, board, (await levels(p)).get(board.projectId))) throw new ApiError("permission", "You need edit on this project to add issues");
  const stage = await stageOfBoard(board.id, input.stageId);
  const assignees = checkAssignees(p, input.assigneeIds ?? []);
  await checkAssignable(board, assignees);
  const now = new Date();
  const id = newId("crd");
  await getDb().transaction(async (tx) => {
    await tx.insert(schema.boardCard).values({
      id,
      boardId: board.id,
      projectId: board.projectId,
      number: await nextCardNumber(tx, board.projectId),
      stageId: stage.id,
      position: await endPosition(tx, stage.id),
      title: checkTitle(input.title),
      titleEdited: true,
      description: checkDescription(input.description ?? ""),
      priority: input.priority ?? "normal",
      labels: checkLabels(input.labels ?? []),
      assigneeIds: assignees,
      dueAt: input.dueAt ? parseDate(input.dueAt) : null,
      sessionIds: addTo([], input.sessionId ?? ""),
      createdBy: p.user.id,
      createdAt: now,
      updatedAt: now,
    });
    await note(tx, id, p.user.id, "created", `Created in ${stage.name}`);
  });
  return cardView(p, id);
}

export async function updateCard(
  p: Principal,
  id: string,
  patch: { title?: string; description?: string; sessionId?: string; priority?: PriorityName; labels?: string[]; assigneeIds?: string[]; dueAt?: string },
): Promise<CardView> {
  const row = await workable(p, id);
  const c = schema.boardCard;
  const set: Omit<Partial<CardRow>, "version"> & { version: SQL } = { updatedAt: new Date(), version: sql`${c.version} + 1` };
  const notes: [string, string][] = [];
  if (patch.title !== undefined && patch.title !== row.title) {
    set.title = checkTitle(patch.title);
    set.titleEdited = true;
    notes.push(["edited", `Title: ${set.title}`]);
  }
  if (patch.description !== undefined && patch.description !== row.description) {
    set.description = checkDescription(patch.description);
    notes.push(["edited", "Changed the description"]);
  }
  if (patch.priority !== undefined && patch.priority !== row.priority) {
    set.priority = patch.priority;
    notes.push(["priority", `Priority: ${patch.priority}`]);
  }
  if (patch.labels !== undefined) {
    set.labels = checkLabels(patch.labels);
    notes.push(["labeled", `Labels: ${(set.labels as string[]).join(", ") || "none"}`]);
  }
  if (patch.assigneeIds !== undefined) {
    const list = checkAssignees(p, patch.assigneeIds);
    await checkAssignable(row._board, list);
    set.assigneeIds = list;
    notes.push(["assigned", list.length ? `Assigned ${list.length}` : "Unassigned"]);
  }
  if (patch.dueAt !== undefined) {
    set.dueAt = patch.dueAt ? parseDate(patch.dueAt) : null;
    notes.push(["due", patch.dueAt ? `Due ${patch.dueAt}` : "No due date"]);
  }
  if (patch.sessionId) set.sessionIds = addTo(row.sessionIds, patch.sessionId);
  await getDb().transaction(async (tx) => {
    await tx.update(c).set(set).where(eq(c.id, row.id));
    for (const [kind, summary] of notes) await note(tx, row.id, p.user.id, kind, summary);
  });
  return cardView(p, row.id);
}

/** Between the neighbours, or at the end. Runs out only after very many moves into one gap; then the stage is renumbered. */
async function positionBefore(tx: Tx, stageId: string, cardId: string, beforeId: string): Promise<number> {
  const c = schema.boardCard;
  if (!beforeId) return endPosition(tx, stageId);
  const rows = await tx.select({ id: c.id, position: c.position }).from(c).where(eq(c.stageId, stageId)).orderBy(asc(c.position));
  const others = rows.filter((r) => r.id !== cardId);
  const at = others.findIndex((r) => r.id === beforeId);
  if (at < 0) return endPosition(tx, stageId);
  const next = others[at]!.position;
  const prev = at > 0 ? others[at - 1]!.position : next - 2 * POSITION_GAP;
  if (next - prev > 1e-6) return (prev + next) / 2;
  for (const [i, r] of others.entries()) await tx.update(c).set({ position: (i + 1) * POSITION_GAP }).where(eq(c.id, r.id));
  return (at + 0.5) * POSITION_GAP;
}

async function supersedeOpen(tx: Tx, cardId: string): Promise<void> {
  const co = schema.boardCardOutput;
  await tx.update(co).set({ status: "superseded", leaseUntil: null, updatedAt: new Date() }).where(and(eq(co.cardId, cardId), inArray(co.status, ["pending", "failed"])));
}

export async function moveCard(p: Principal, idOrKey: string, input: { stageId: string; beforeCardId?: string; expectedVersion?: number; sessionId?: string }): Promise<CardView> {
  const row = await workable(p, idOrKey);
  const id = row.id;
  const stage = await stageOfBoard(row.boardId, input.stageId);
  const c = schema.boardCard;
  const moved = await getDb().transaction(async (tx) => {
    const position = await positionBefore(tx, stage.id, id, input.beforeCardId ?? "");
    // The version in the WHERE makes it atomic: of two moves from the same version, one finds nothing to update.
    const conds = [eq(c.id, id)];
    if (input.expectedVersion) conds.push(eq(c.version, input.expectedVersion));
    const [updated] = await tx
      .update(c)
      .set({ stageId: stage.id, position, version: sql`${c.version} + 1`, updatedAt: new Date(), sessionIds: addTo(row.sessionIds, input.sessionId ?? "") })
      .where(and(...conds))
      .returning();
    if (!updated) throw new ApiError("failed_precondition", "Someone else moved this issue first. Reload the board.");
    if (row.stageId === stage.id) return false;
    await note(tx, id, p.user.id, "moved", `Moved to ${stage.name}`);
    await supersedeOpen(tx, id);
    if (updated.connectionId && updated.externalState !== "disconnected") {
      const o = schema.boardStageOutput;
      const outs = await tx
        .select()
        .from(o)
        .where(and(eq(o.stageId, stage.id), eq(o.connectionScope, updated.connectionScope), eq(o.connectionId, updated.connectionId)));
      const now = new Date();
      for (const out of outs) {
        await tx.insert(schema.boardCardOutput).values({
          id: newId("cou"),
          cardId: id,
          stageId: stage.id,
          connectionScope: out.connectionScope,
          connectionId: out.connectionId,
          fields: out.fields,
          comment: out.comment,
          status: "pending",
          nextAttemptAt: now,
          createdAt: now,
          updatedAt: now,
        });
      }
      return outs.length > 0;
    }
    return false;
  });
  if (moved) kickOutputs();
  return cardView(p, id);
}

export async function deleteCard(p: Principal, id: string): Promise<void> {
  const row = await workable(p, id);
  await getDb().delete(schema.boardCard).where(eq(schema.boardCard.id, row.id));
}

export async function retryCardOutput(p: Principal, cardId: string): Promise<CardView> {
  const row = await workable(p, cardId);
  const co = schema.boardCardOutput;
  const [updated] = await getDb()
    .update(co)
    .set({ status: "pending", attempts: 0, nextAttemptAt: new Date(), leaseUntil: null, updatedAt: new Date() })
    .where(and(eq(co.cardId, row.id), eq(co.status, "failed")))
    .returning();
  if (!updated) throw new ApiError("failed_precondition", "This issue has no failed output");
  kickOutputs();
  return cardView(p, row.id);
}

export async function linkCard(
  p: Principal,
  idOrKey: string,
  input: { connection?: { scope: ConnScope; id: string }; externalId?: string; externalUrl?: string; externalTitle?: string; unlinkExternal?: boolean; sessionId?: string; branch?: string; prUrl?: string },
): Promise<CardView> {
  const row = await workable(p, idOrKey);
  const id = row.id;
  const c = schema.boardCard;
  const set: Omit<Partial<CardRow>, "version"> & { version: SQL } = { updatedAt: new Date(), version: sql`${c.version} + 1` };
  const notes: string[] = [];
  if (input.sessionId) {
    set.sessionIds = addTo(row.sessionIds, input.sessionId);
    if (!(row.sessionIds as string[]).includes(input.sessionId)) notes.push("Linked a session");
  }
  if (input.branch !== undefined && input.branch !== "") {
    set.branch = input.branch.slice(0, 300);
    notes.push(`Branch ${set.branch}`);
  }
  if (input.prUrl) {
    if (!/^https?:\/\//.test(input.prUrl)) throw new ApiError("invalid_request", "pr_url must be an http(s) URL");
    set.prUrls = addTo(row.prUrls, input.prUrl);
    notes.push(`Pull request ${input.prUrl}`);
  }
  if (input.unlinkExternal) {
    Object.assign(set, { connectionScope: "", connectionId: "", externalId: "", externalUrl: "", externalState: "", externalCheckedAt: null });
    notes.push("Unlinked the external issue");
  } else if (input.externalId) {
    const conn = input.connection;
    if (!conn?.id || (conn.scope !== "admin" && conn.scope !== "device")) throw new ApiError("invalid_request", "Choose a connection for the issue");
    if (row._board.scope === "shared" && conn.scope === "device") throw new ApiError("invalid_request", "A shared board uses company connections only");
    if (conn.scope === "admin" && !(await connectionsById([conn.id])).has(conn.id)) throw new ApiError("invalid_request", `No connection ${conn.id}`);
    if (input.externalUrl && !/^https?:\/\//.test(input.externalUrl)) throw new ApiError("invalid_request", "external_url must be an http(s) URL");
    Object.assign(set, { connectionScope: conn.scope, connectionId: conn.id, externalId: input.externalId, externalUrl: input.externalUrl ?? "", externalState: "linked", externalCheckedAt: new Date() });
    if (input.externalTitle && !row.titleEdited) set.title = checkTitle(input.externalTitle);
    await getDb()
      .insert(schema.boardInputSeen)
      .values({ boardId: row.boardId, connectionScope: conn.scope, connectionId: conn.id, externalId: input.externalId, firstSeenAt: new Date() })
      .onConflictDoNothing();
    notes.push(`Linked ${input.externalId}`);
  }
  await getDb().transaction(async (tx) => {
    await tx.update(c).set(set).where(eq(c.id, id));
    for (const n of notes) await note(tx, id, p.user.id, "linked", n);
  });
  return cardView(p, id);
}

export async function relateCard(p: Principal, idOrKey: string, input: { kind: RelationView["kind"]; other: string; remove?: boolean }): Promise<CardView> {
  const row = await workable(p, idOrKey);
  const other = (await cardOf(p, input.other)).card;
  if (other.id === row.id) throw new ApiError("invalid_request", "An issue cannot relate to itself");
  // "blocked by" is "blocks" the other way round.
  const from = input.kind === "blocked_by" ? other.id : row.id;
  const to = input.kind === "blocked_by" ? row.id : other.id;
  const kind = input.kind === "blocked_by" ? "blocks" : input.kind;
  const r = schema.boardCardRelation;
  await getDb().transaction(async (tx) => {
    if (input.remove) {
      await tx.delete(r).where(and(eq(r.cardId, from), eq(r.kind, kind), eq(r.otherId, to)));
    } else {
      if (kind === "blocks") {
        const back = await tx.select().from(r).where(and(eq(r.cardId, to), eq(r.kind, "blocks"), eq(r.otherId, from)));
        if (back.length) throw new ApiError("failed_precondition", "These issues would block each other");
      }
      await tx.insert(r).values({ cardId: from, kind, otherId: to }).onConflictDoNothing();
    }
    await note(tx, row.id, p.user.id, "related", `${input.remove ? "Removed" : "Added"} ${input.kind.replace("_", " ")} ${input.other}`);
  });
  return cardView(p, row.id);
}

export interface CommentView {
  id: string;
  authorId: string;
  authorName: string;
  text: string;
  createdAt: string;
}

async function namesOf(ids: string[]): Promise<Map<string, string>> {
  const list = [...new Set(ids.filter(Boolean))];
  if (!list.length) return new Map();
  const u = schema.user;
  const rows = await getDb().select({ id: u.id, name: u.name, email: u.email }).from(u).where(inArray(u.id, list));
  return new Map(rows.map((r) => [r.id, r.name || r.email]));
}

export async function addCardComment(p: Principal, idOrKey: string, text: string, sessionId?: string): Promise<CommentView> {
  const row = await workable(p, idOrKey);
  const body = text.trim();
  if (!body) throw new ApiError("invalid_request", "text is required");
  if (body.length > DESCRIPTION_MAX) throw new ApiError("invalid_request", `Comments have at most ${DESCRIPTION_MAX} characters`);
  const now = new Date();
  const id = newId("cmt");
  const c = schema.boardCard;
  await getDb().transaction(async (tx) => {
    await tx.insert(schema.boardCardComment).values({ id, cardId: row.id, authorId: p.user.id, text: body, createdAt: now });
    await tx
      .update(c)
      .set({ commentCount: sql`${c.commentCount} + 1`, updatedAt: now, sessionIds: addTo(row.sessionIds, sessionId ?? "") })
      .where(eq(c.id, row.id));
    await note(tx, row.id, p.user.id, "commented", "Commented");
  });
  return { id, authorId: p.user.id, authorName: p.user.name || p.user.email, text: body, createdAt: now.toISOString() };
}

export async function deleteCardComment(p: Principal, commentId: string): Promise<void> {
  const cm = schema.boardCardComment;
  const row = await getDb().query.boardCardComment.findFirst({ where: eq(cm.id, commentId) });
  if (!row) throw new ApiError("not_found", "Comment not found");
  const { board, level } = await cardOf(p, row.cardId);
  if (row.authorId !== p.user.id && !canConfigure(p, board, level)) throw new ApiError("permission", "Only its author or those with manage remove a comment");
  const c = schema.boardCard;
  await getDb().transaction(async (tx) => {
    await tx.delete(cm).where(eq(cm.id, commentId));
    await tx
      .update(c)
      .set({ commentCount: sql`greatest(${c.commentCount} - 1, 0)` })
      .where(eq(c.id, row.cardId));
  });
}

export async function cardHistory(cardId: string): Promise<{ comments: CommentView[]; activity: { id: string; actorId: string; actorName: string; kind: string; summary: string; createdAt: string }[] }> {
  const db = getDb();
  const cm = schema.boardCardComment;
  const ac = schema.boardCardActivity;
  const comments = await db.select().from(cm).where(eq(cm.cardId, cardId)).orderBy(asc(cm.createdAt)).limit(500);
  const activity = await db.select().from(ac).where(eq(ac.cardId, cardId)).orderBy(asc(ac.createdAt)).limit(500);
  const names = await namesOf([...comments.map((c) => c.authorId), ...activity.map((a) => a.actorId)]);
  return {
    comments: comments.map((c) => ({ id: c.id, authorId: c.authorId, authorName: names.get(c.authorId) ?? "", text: c.text, createdAt: c.createdAt.toISOString() })),
    activity: activity.map((a) => ({ id: a.id, actorId: a.actorId, actorName: names.get(a.actorId) ?? "", kind: a.kind, summary: a.summary, createdAt: a.createdAt.toISOString() })),
  };
}

// ---- saved views ----

export type SavedViewRow = typeof schema.savedView.$inferSelect;

export async function listSavedViews(p: Principal): Promise<SavedViewRow[]> {
  await ensureDb();
  const v = schema.savedView;
  return getDb().select().from(v).where(eq(v.ownerId, p.user.id)).orderBy(asc(v.name));
}

export async function saveView(p: Principal, input: { id?: string; name: string; filter: CardFilter }): Promise<SavedViewRow> {
  await ensureDb();
  const name = checkName(input.name, "A view");
  const v = schema.savedView;
  const now = new Date();
  if (input.id) {
    const [row] = await getDb()
      .update(v)
      .set({ name, filter: input.filter, updatedAt: now })
      .where(and(eq(v.id, input.id), eq(v.ownerId, p.user.id)))
      .returning();
    if (!row) throw new ApiError("not_found", "View not found");
    return row;
  }
  const row: SavedViewRow = { id: newId("view"), ownerId: p.user.id, name, filter: input.filter, updatedAt: now };
  await getDb().insert(v).values(row);
  return row;
}

export async function deleteSavedView(p: Principal, id: string): Promise<void> {
  await ensureDb();
  const v = schema.savedView;
  await getDb().delete(v).where(and(eq(v.id, id), eq(v.ownerId, p.user.id)));
}

export async function prepareCardSession(p: Principal, id: string): Promise<{ branch: string; prompt: string; projectId: string; key: string }> {
  const { card: row } = await cardOf(p, id);
  const key = keyOf(row, (await tickers([row.projectId])).get(row.projectId) ?? "");
  const slug = row.title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/, "");
  const branch = `issue/${key.toLowerCase()}${slug ? `-${slug}` : ""}`;
  const lines = [`Work on ${key}: ${row.title}`];
  if (row.description) lines.push("", row.description);
  lines.push("", `Read it with the Issues tool first${row.externalUrl ? `; it is on ${row.externalUrl}` : ""}.`);
  return { branch, prompt: lines.join("\n"), projectId: row.projectId, key };
}

/** The session's PR goes on every card the session is linked to. */
export async function linkSessionPullRequest(p: Principal, sessionId: string, prUrl: string): Promise<string[]> {
  if (!sessionId || !prUrl) throw new ApiError("invalid_request", "session_id and pr_url are required");
  if (!/^https?:\/\//.test(prUrl)) throw new ApiError("invalid_request", "pr_url must be an http(s) URL");
  const cards = await listCards(p, { sessionId }, 100);
  const c = schema.boardCard;
  const out: string[] = [];
  for (const { row } of cards) {
    if (((row.prUrls ?? []) as string[]).includes(prUrl)) continue;
    await getDb()
      .update(c)
      .set({ prUrls: addTo(row.prUrls, prUrl), updatedAt: new Date(), version: sql`${c.version} + 1` })
      .where(eq(c.id, row.id));
    out.push(row.id);
  }
  return out;
}

// The sync registers itself: board-sync.ts imports this file, and a move only needs to wake it.
let kick: () => void = () => {};
export function onOutputsPending(fn: () => void): void {
  kick = fn;
}
function kickOutputs(): void {
  kick();
}

export { visible as visibleBoard };
