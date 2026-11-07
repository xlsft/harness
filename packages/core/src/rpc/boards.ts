/** Connect implementation of harness.library.v1 BoardService and TrackerConnectionService: proto <-> src/services/boards.ts and src/tracker. */
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import {
  BoardScope,
  BoardService,
  CardOutputStatus,
  CardPriority,
  CardRelationKind,
  ExternalState,
  type BoardStage,
  type BoardStageSchema,
  type CardFilter as PbCardFilter,
  type CardFilterSchema,
  type CardSchema,
  type BoardSchema,
} from "../gen/harness/library/v1/boards_pb.ts";
import {
  ProjectLevel,
  ProjectScope,
  ProjectService,
  ProjectShareSubject,
  type ProjectMetadata,
  type ProjectSchema,
  type ProjectShare,
} from "../gen/harness/library/v1/projects_pb.ts";
import {
  TrackerAuthMethod,
  TrackerConnectionScope,
  TrackerConnectionService,
  type TrackerCapabilitiesSchema,
  type TrackerConnectionInput,
  type TrackerConnectionRef,
  type TrackerConnectionSchema,
  type TrackerIssueSchema,
} from "../gen/harness/library/v1/tracker_connections_pb.ts";
import { ApiError } from "../lib/errors.ts";
import {
  addCardComment,
  cardHistory,
  createBoard,
  createCard,
  deleteBoard,
  deleteCard,
  deleteCardComment,
  deleteSavedView,
  getBoard,
  getCard,
  linkCard,
  linkSessionPullRequest,
  listBoards,
  listCards,
  listSavedViews,
  moveCard,
  prepareCardSession,
  relateCard,
  retryCardOutput,
  saveView,
  updateBoard,
  updateCard,
  type BoardView,
  type CardFilter,
  type CardView,
  type ConnScope,
  type FieldSet,
  type PriorityName,
  type RelationView,
  type SavedViewRow,
  type StageSpec,
} from "../services/boards.ts";
import {
  createProject,
  deleteProject,
  ensureDefaultProject,
  getProject,
  listProjectMembers,
  listProjects,
  listProviderProjects,
  setProjectShares,
  updateProject,
  type Level,
  type Metadata,
  type ProjectView,
  type ShareSubject,
} from "../services/projects.ts";
import { claimDeviceWork, forgetDeviceConnection, reportDeviceWork, syncBoard } from "../services/board-sync.ts";
import type { Principal } from "../services/devices.ts";
import {
  deleteConnection,
  getConnection,
  hasSecret,
  listConnectionFields,
  listConnections,
  openProvider,
  PROVIDERS,
  providerMessage,
  putConnection,
  searchConnectionIssues,
  startOAuth,
  testConnection,
  type ConnectionInput,
  type ConnectionRow,
} from "../tracker/connections.ts";
import type { AuthMethodName, Capabilities, Issue } from "../tracker/provider.ts";
import { TrackerError } from "../tracker/provider.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

const AUTH: Record<AuthMethodName, TrackerAuthMethod> = { token: TrackerAuthMethod.TOKEN, password: TrackerAuthMethod.PASSWORD, oauth: TrackerAuthMethod.OAUTH };

function authName(m: TrackerAuthMethod): AuthMethodName {
  if (m === TrackerAuthMethod.TOKEN) return "token";
  if (m === TrackerAuthMethod.PASSWORD) return "password";
  if (m === TrackerAuthMethod.OAUTH) return "oauth";
  throw new ApiError("invalid_request", "Choose how this connection signs in");
}

function capabilitiesToProto(c: Capabilities): MessageInitShape<typeof TrackerCapabilitiesSchema> {
  return {
    provider: c.provider,
    displayName: c.displayName,
    authMethods: c.authMethods.map((m) => AUTH[m]),
    queryLanguage: c.queryLanguage,
    queryExample: c.queryExample,
    customFields: c.customFields,
    comments: c.comments,
    knowledge: c.knowledge,
  };
}

/** Everyone sees that a company connection exists and works; how it signs in, and why it fails, only admins. */
function connectionToProto(r: ConnectionRow, admin: boolean): MessageInitShape<typeof TrackerConnectionSchema> {
  const capabilities = PROVIDERS.find((c) => c.provider === r.provider);
  return {
    id: r.id,
    scope: TrackerConnectionScope.ADMIN,
    provider: r.provider,
    name: r.name,
    enabled: r.enabled,
    knowledgeEnabled: r.knowledgeEnabled,
    healthy: r.healthy,
    capabilities: capabilities && capabilitiesToProto(capabilities),
    ...(admin
      ? {
          baseUrl: r.baseUrl,
          auth: AUTH[r.auth as AuthMethodName],
          login: r.login,
          oauthClientId: r.oauthClientId,
          hasSecret: hasSecret(r),
          allowPrivateNetwork: r.allowPrivateNetwork,
          knowledgeQuery: r.knowledgeQuery,
          lastError: r.lastError,
          lastCheckedAt: r.lastCheckedAt?.toISOString() ?? "",
        }
      : {}),
  };
}

function connectionFromProto(c: TrackerConnectionInput | undefined): ConnectionInput {
  if (!c) throw new ApiError("invalid_request", "connection is required");
  return {
    id: c.id || undefined,
    provider: c.provider,
    name: c.name,
    baseUrl: c.baseUrl,
    auth: authName(c.auth),
    login: c.login,
    secret: c.secret,
    oauthClientId: c.oauthClientId,
    oauthClientSecret: c.oauthClientSecret,
    enabled: c.enabled,
    allowPrivateNetwork: c.allowPrivateNetwork,
    knowledgeEnabled: c.knowledgeEnabled,
    knowledgeQuery: c.knowledgeQuery,
  };
}

function issueToProto(i: Issue): MessageInitShape<typeof TrackerIssueSchema> {
  return { id: i.id, title: i.title, url: i.url, description: i.description, project: i.project, fields: i.fields, updatedAt: i.updatedAt, comments: i.comments };
}

function scopeName(s: TrackerConnectionScope): ConnScope {
  if (s === TrackerConnectionScope.ADMIN) return "admin";
  if (s === TrackerConnectionScope.DEVICE) return "device";
  throw new ApiError("invalid_request", "Choose a connection");
}

const refFrom = (r: TrackerConnectionRef | undefined): { scope: ConnScope; id: string } => ({ scope: scopeName(r?.scope ?? TrackerConnectionScope.UNSPECIFIED), id: r?.id ?? "" });
const refTo = (scope: string, id: string) => ({ scope: scope === "admin" ? TrackerConnectionScope.ADMIN : TrackerConnectionScope.DEVICE, id });
const isoOf = (d: Date | null | undefined) => d?.toISOString() ?? "";

function boardToProto(b: BoardView): MessageInitShape<typeof BoardSchema> {
  return {
    id: b.row.id,
    scope: b.row.scope === "shared" ? BoardScope.SHARED : BoardScope.PERSONAL,
    name: b.row.name,
    projectId: b.row.projectId,
    canConfigure: b.canConfigure,
    canEdit: b.canEdit,
    updatedAt: b.row.updatedAt.toISOString(),
    stages: b.stages.map(
      (s): MessageInitShape<typeof BoardStageSchema> => ({
        id: s.row.id,
        name: s.row.name,
        inputs: s.inputs.map((i) => ({
          id: i.id,
          connection: refTo(i.connectionScope, i.connectionId),
          query: i.query,
          intervalSeconds: i.intervalSeconds,
          lastPolledAt: isoOf(i.lastPolledAt),
          lastOkAt: isoOf(i.lastOkAt),
          lastError: i.lastError,
        })),
        outputs: s.outputs.map((o) => ({ id: o.id, connection: refTo(o.connectionScope, o.connectionId), fields: (o.fields ?? []) as FieldSet[], comment: o.comment })),
      }),
    ),
  };
}

const OUTPUT_STATUS: Record<string, CardOutputStatus> = { pending: CardOutputStatus.PENDING, running: CardOutputStatus.RUNNING, done: CardOutputStatus.DONE, failed: CardOutputStatus.FAILED };
const EXTERNAL_STATE: Record<string, ExternalState> = { linked: ExternalState.LINKED, missing: ExternalState.MISSING, disconnected: ExternalState.DISCONNECTED };

const PRIORITY: Record<PriorityName, CardPriority> = { low: CardPriority.LOW, normal: CardPriority.NORMAL, high: CardPriority.HIGH, urgent: CardPriority.URGENT };
const PRIORITY_NAME: Partial<Record<CardPriority, PriorityName>> = { [CardPriority.LOW]: "low", [CardPriority.NORMAL]: "normal", [CardPriority.HIGH]: "high", [CardPriority.URGENT]: "urgent" };
const RELATION: Record<RelationView["kind"], CardRelationKind> = { blocks: CardRelationKind.BLOCKS, blocked_by: CardRelationKind.BLOCKED_BY, relates: CardRelationKind.RELATES, duplicates: CardRelationKind.DUPLICATES };

function priorityOf(v: CardPriority): PriorityName | undefined {
  if (v === CardPriority.UNSPECIFIED) return undefined;
  const n = PRIORITY_NAME[v];
  if (!n) throw new ApiError("invalid_request", "Unknown priority");
  return n;
}

function relationOf(v: CardRelationKind): RelationView["kind"] {
  const found = (Object.entries(RELATION) as [RelationView["kind"], CardRelationKind][]).find(([, k]) => k === v);
  if (!found) throw new ApiError("invalid_request", "Choose a relation");
  return found[0];
}

function filterFrom(f: PbCardFilter | undefined): CardFilter {
  if (!f) return {};
  return {
    projectId: f.projectId,
    boardId: f.boardId,
    stageId: f.stageId,
    query: f.query,
    assigneeIds: f.assigneeIds,
    unassigned: f.unassigned,
    labels: f.labels,
    priorities: f.priorities.map(priorityOf).filter((x): x is PriorityName => !!x),
    dueBefore: f.dueBefore,
    sessionId: f.sessionId,
    blocked: f.blocked,
    sort: f.sort,
  };
}

function filterTo(f: CardFilter): MessageInitShape<typeof CardFilterSchema> {
  return { ...f, priorities: (f.priorities ?? []).map((x) => PRIORITY[x]) };
}

const viewToProto = (v: SavedViewRow) => ({ id: v.id, name: v.name, filter: filterTo((v.filter ?? {}) as CardFilter), updatedAt: v.updatedAt.toISOString() });

const LEVEL: Record<Level, ProjectLevel> = { view: ProjectLevel.VIEW, edit: ProjectLevel.EDIT, manage: ProjectLevel.MANAGE };
const SUBJECT: Record<ShareSubject, ProjectShareSubject> = { user: ProjectShareSubject.USER, group: ProjectShareSubject.GROUP, role: ProjectShareSubject.ROLE, everyone: ProjectShareSubject.EVERYONE };

function sharesFrom(list: ProjectShare[]): { subject: ShareSubject; subjectId: string; level: Level }[] {
  return list.map((s) => {
    const subject = (Object.entries(SUBJECT) as [ShareSubject, ProjectShareSubject][]).find(([, v]) => v === s.subject)?.[0];
    const level = (Object.entries(LEVEL) as [Level, ProjectLevel][]).find(([, v]) => v === s.level)?.[0];
    if (!subject || !level) throw new ApiError("invalid_request", "A share names a subject and a level");
    return { subject, subjectId: s.subjectId, level };
  });
}

const metaFrom = (m: ProjectMetadata | undefined): Partial<Metadata> => (m ? { ticker: m.ticker, name: m.name, description: m.description, logo: m.logo, color: m.color } : {});

function projectToProto(v: ProjectView): MessageInitShape<typeof ProjectSchema> {
  return {
    id: v.row.id,
    scope: v.row.scope === "global" ? ProjectScope.GLOBAL : ProjectScope.PERSONAL,
    metadata: v.metadata,
    local: v.local,
    provider: v.provider,
    connection: v.row.connectionId ? refTo(v.row.connectionScope, v.row.connectionId) : undefined,
    externalKey: v.row.externalKey,
    ownerId: v.row.ownerId ?? "",
    level: LEVEL[v.level],
    shares: v.shares.map((s) => ({ subject: SUBJECT[s.subject as ShareSubject], subjectId: s.subjectId, level: LEVEL[s.level as Level], subjectName: s.subjectName })),
    createdAt: v.row.createdAt.toISOString(),
    updatedAt: v.row.updatedAt.toISOString(),
  };
}

function cardToProto({ row: c, output: o, key, relations }: CardView): MessageInitShape<typeof CardSchema> {
  return {
    id: c.id,
    key,
    projectId: c.projectId,
    priority: PRIORITY[c.priority as PriorityName] ?? CardPriority.NORMAL,
    labels: (c.labels ?? []) as string[],
    assigneeIds: (c.assigneeIds ?? []) as string[],
    dueAt: isoOf(c.dueAt),
    commentCount: c.commentCount,
    relations: relations.map((r) => ({ kind: RELATION[r.kind], cardId: r.cardId, key: r.key, title: r.title })),
    boardId: c.boardId,
    stageId: c.stageId,
    title: c.title,
    description: c.description,
    version: c.version,
    external: c.externalId ? { connection: refTo(c.connectionScope, c.connectionId), externalId: c.externalId, url: c.externalUrl, state: EXTERNAL_STATE[c.externalState] ?? ExternalState.UNSPECIFIED, checkedAt: isoOf(c.externalCheckedAt) } : undefined,
    output: o
      ? {
          id: o.id,
          stageId: o.stageId,
          connection: refTo(o.connectionScope, o.connectionId),
          status: OUTPUT_STATUS[o.status] ?? CardOutputStatus.UNSPECIFIED,
          error: o.lastError,
          attempts: o.attempts,
          updatedAt: o.updatedAt.toISOString(),
          fields: (o.fields ?? []) as FieldSet[],
        }
      : undefined,
    sessionIds: (c.sessionIds ?? []) as string[],
    prUrls: (c.prUrls ?? []) as string[],
    branch: c.branch,
    createdBy: c.createdBy,
    createdAt: c.createdAt.toISOString(),
    updatedAt: c.updatedAt.toISOString(),
  };
}

function stagesFromProto(stages: BoardStage[]): StageSpec[] {
  return stages.map((s) => ({
    id: s.id || undefined,
    name: s.name,
    inputs: s.inputs.map((i) => ({ id: i.id || undefined, connection: refFrom(i.connection), query: i.query, intervalSeconds: i.intervalSeconds })),
    outputs: s.outputs.map((o) => ({ id: o.id || undefined, connection: refFrom(o.connection), fields: o.fields.map((f) => ({ name: f.name, value: f.value })), comment: o.comment })),
  }));
}

/** The linked issue, live, when this server holds the connection. */
async function linkedIssue(p: Principal, v: CardView): Promise<{ issue?: MessageInitShape<typeof TrackerIssueSchema>; issueError: string }> {
  if (!v.row.externalId || v.row.connectionScope !== "admin" || v.row.externalState === "disconnected") return { issueError: "" };
  const conn = await getConnection(v.row.connectionId);
  if (!conn?.enabled) return { issueError: "The connection is not available" };
  try {
    return { issue: issueToProto(await openProvider(conn).get(v.row.externalId)), issueError: "" };
  } catch (err) {
    return { issueError: p.user.role === "admin" || err instanceof TrackerError ? providerMessage(err) : "The issue could not be read" };
  }
}

export function registerBoardServices(router: ConnectRouter): void {
  router.service(TrackerConnectionService, {
    listTrackerConnections: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const admin = p.user.role === "admin";
      const rows = await listConnections();
      // A disabled connection is an admin's matter: nobody else can pick it.
      return { connections: rows.filter((r) => admin || r.enabled).map((r) => connectionToProto(r, admin)), providers: PROVIDERS.map(capabilitiesToProto) };
    }),
    putTrackerConnection: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return { connection: connectionToProto(await putConnection(p, connectionFromProto(req.connection)), true) };
    }),
    deleteTrackerConnection: guard(async (req, ctx: HandlerContext) => {
      await deleteConnection(await requirePrincipal(ctx), req.id);
      return {};
    }),
    testTrackerConnection: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return await testConnection(p, req.id, req.draft ? connectionFromProto(req.draft) : undefined);
    }),
    searchConnectionIssues: guard(async (req, ctx: HandlerContext) => {
      await requirePrincipal(ctx);
      const r = await searchConnectionIssues(req.connectionId, req.query, req.limit);
      return { issues: r.issues.map(issueToProto), hasMore: r.hasMore };
    }),
    listConnectionFields: guard(async (req, ctx: HandlerContext) => {
      await requirePrincipal(ctx);
      return { fields: await listConnectionFields(req.connectionId, req.project) };
    }),
    startTrackerOAuth: guard(async (req, ctx: HandlerContext) => ({ authorizeUrl: await startOAuth(await requirePrincipal(ctx), req.connectionId) })),
  });

  router.service(ProjectService, {
    listProjects: guard(async (_req, ctx: HandlerContext) => ({ projects: (await listProjects(await requirePrincipal(ctx))).map(projectToProto) })),
    getProject: guard(async (req, ctx: HandlerContext) => ({ project: projectToProto(await getProject(await requirePrincipal(ctx), req.id)) })),
    createProject: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const scope = req.scope === ProjectScope.GLOBAL ? "global" : req.scope === ProjectScope.PERSONAL ? "personal" : undefined;
      if (!scope) throw new ApiError("invalid_request", "A project is personal or global");
      return {
        project: projectToProto(
          await createProject(p, {
            scope,
            metadata: metaFrom(req.metadata),
            connection: req.connection?.id ? refFrom(req.connection) : undefined,
            externalKey: req.externalKey,
            provider: metaFrom(req.provider),
            shares: req.shares.length ? sharesFrom(req.shares) : undefined,
          }),
        ),
      };
    }),
    updateProject: guard(async (req, ctx: HandlerContext) => ({
      project: projectToProto(await updateProject(await requirePrincipal(ctx), req.id, { ticker: req.ticker, name: req.name, description: req.description, logo: req.logo, color: req.color, providerValues: req.provider ? metaFrom(req.provider) : undefined })),
    })),
    deleteProject: guard(async (req, ctx: HandlerContext) => {
      await deleteProject(await requirePrincipal(ctx), req.id);
      return {};
    }),
    setProjectShares: guard(async (req, ctx: HandlerContext) => ({ project: projectToProto(await setProjectShares(await requirePrincipal(ctx), req.id, sharesFrom(req.shares))) })),
    ensureDefaultProject: guard(async (_req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const v = await ensureDefaultProject(p);
      if (!(await listBoards(p, v.row.id)).length) await createBoard(p, { projectId: v.row.id, scope: "personal", name: "Board" });
      return { project: projectToProto(v) };
    }),
    listProviderProjects: guard(async (req, ctx: HandlerContext) => {
      await requirePrincipal(ctx);
      return { projects: await listProviderProjects(req.connectionId) };
    }),
    listProjectMembers: guard(async (req, ctx: HandlerContext) => ({ members: (await listProjectMembers(await requirePrincipal(ctx), req.id)).map((m) => ({ ...m, level: LEVEL[m.level] })) })),
  });

  router.service(BoardService, {
    listBoards: guard(async (req, ctx: HandlerContext) => ({ boards: (await listBoards(await requirePrincipal(ctx), req.projectId)).map(boardToProto) })),
    getBoard: guard(async (req, ctx: HandlerContext) => {
      const r = await getBoard(await requirePrincipal(ctx), req.id);
      return { board: boardToProto(r.board), cards: r.cards.map(cardToProto) };
    }),
    createBoard: guard(async (req, ctx: HandlerContext) => {
      const scope = req.scope === BoardScope.SHARED ? "shared" : req.scope === BoardScope.PERSONAL ? "personal" : undefined;
      if (!scope) throw new ApiError("invalid_request", "A board is shared or personal");
      return { board: boardToProto(await createBoard(await requirePrincipal(ctx), { projectId: req.projectId, scope, name: req.name, stages: stagesFromProto(req.stages) })) };
    }),
    updateBoard: guard(async (req, ctx: HandlerContext) => ({
      board: boardToProto(await updateBoard(await requirePrincipal(ctx), req.id, { name: req.name, stages: stagesFromProto(req.stages) })),
    })),
    deleteBoard: guard(async (req, ctx: HandlerContext) => {
      await deleteBoard(await requirePrincipal(ctx), req.id);
      return {};
    }),
    listCards: guard(async (req, ctx: HandlerContext) => ({ cards: (await listCards(await requirePrincipal(ctx), filterFrom(req.filter), req.limit)).map(cardToProto) })),
    getCard: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      const v = await getCard(p, req.id);
      return { card: cardToProto(v), ...(await linkedIssue(p, v)), ...(await cardHistory(v.row.id)) };
    }),
    createCard: guard(async (req, ctx: HandlerContext) => ({
      card: cardToProto(
        await createCard(await requirePrincipal(ctx), {
          boardId: req.boardId,
          stageId: req.stageId,
          title: req.title,
          description: req.description,
          sessionId: req.sessionId,
          priority: priorityOf(req.priority),
          labels: req.labels,
          assigneeIds: req.assigneeIds,
          dueAt: req.dueAt,
        }),
      ),
    })),
    updateCard: guard(async (req, ctx: HandlerContext) => ({
      card: cardToProto(
        await updateCard(await requirePrincipal(ctx), req.id, {
          title: req.title,
          description: req.description,
          sessionId: req.sessionId,
          priority: req.priority === undefined ? undefined : priorityOf(req.priority),
          labels: req.setLabels ? req.labels : undefined,
          assigneeIds: req.setAssignees ? req.assigneeIds : undefined,
          dueAt: req.dueAt,
        }),
      ),
    })),
    relateCard: guard(async (req, ctx: HandlerContext) => ({ card: cardToProto(await relateCard(await requirePrincipal(ctx), req.id, { kind: relationOf(req.kind), other: req.other, remove: req.remove })) })),
    deleteCardComment: guard(async (req, ctx: HandlerContext) => {
      await deleteCardComment(await requirePrincipal(ctx), req.id);
      return {};
    }),
    listSavedViews: guard(async (_req, ctx: HandlerContext) => ({ views: (await listSavedViews(await requirePrincipal(ctx))).map(viewToProto) })),
    saveView: guard(async (req, ctx: HandlerContext) => ({ view: viewToProto(await saveView(await requirePrincipal(ctx), { id: req.id, name: req.name, filter: filterFrom(req.filter) })) })),
    deleteSavedView: guard(async (req, ctx: HandlerContext) => {
      await deleteSavedView(await requirePrincipal(ctx), req.id);
      return {};
    }),
    moveCard: guard(async (req, ctx: HandlerContext) => ({
      card: cardToProto(await moveCard(await requirePrincipal(ctx), req.id, { stageId: req.stageId, beforeCardId: req.beforeCardId, expectedVersion: req.expectedVersion, sessionId: req.sessionId })),
    })),
    deleteCard: guard(async (req, ctx: HandlerContext) => {
      await deleteCard(await requirePrincipal(ctx), req.id);
      return {};
    }),
    retryCardOutput: guard(async (req, ctx: HandlerContext) => ({ card: cardToProto(await retryCardOutput(await requirePrincipal(ctx), req.cardId)) })),
    linkCard: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      return {
        card: cardToProto(
          await linkCard(p, req.id, {
            connection: req.externalId ? refFrom(req.connection) : undefined,
            externalId: req.externalId,
            externalUrl: req.externalUrl,
            externalTitle: req.externalTitle,
            unlinkExternal: req.unlinkExternal,
            sessionId: req.sessionId,
            branch: req.branch,
            prUrl: req.prUrl,
          }),
        ),
      };
    }),
    commentCard: guard(async (req, ctx: HandlerContext) => {
      const p = await requirePrincipal(ctx);
      if (!req.text.trim()) throw new ApiError("invalid_request", "text is required");
      if (!req.onExternal) return { comment: await addCardComment(p, req.id, req.text, req.sessionId) };
      const v = await getCard(p, req.id);
      if (!v.row.externalId) throw new ApiError("failed_precondition", "This issue has no linked external issue to comment on");
      if (v.row.connectionScope !== "admin") throw new ApiError("failed_precondition", "This issue is on a connection of your device");
      const conn = await getConnection(v.row.connectionId);
      if (!conn?.enabled) throw new ApiError("failed_precondition", "The connection is not available");
      // Editing rights on our side first: the company account must not comment for someone who only views.
      await updateCard(p, v.row.id, { sessionId: req.sessionId });
      try {
        await openProvider(conn).comment(v.row.externalId, req.text);
      } catch (err) {
        throw new ApiError("upstream", providerMessage(err));
      }
      return {};
    }),
    prepareCardSession: guard(async (req, ctx: HandlerContext) => prepareCardSession(await requirePrincipal(ctx), req.id)),
    linkSessionPullRequest: guard(async (req, ctx: HandlerContext) => ({ cardIds: await linkSessionPullRequest(await requirePrincipal(ctx), req.sessionId, req.prUrl) })),
    syncBoard: guard(async (req, ctx: HandlerContext) => ({ newCards: await syncBoard(await requirePrincipal(ctx), req.id) })),
    claimDeviceWork: guard(async (req, ctx: HandlerContext) => claimDeviceWork(await requirePrincipal(ctx), req.connectionIds)),
    reportDeviceWork: guard(async (req, ctx: HandlerContext) => ({
      newCards: await reportDeviceWork(await requirePrincipal(ctx), {
        inputs: req.inputs.map((i) => ({ inputId: i.inputId, error: i.error, issues: i.issues.map((x) => ({ id: x.id, title: x.title, url: x.url })) })),
        outputs: req.outputs.map((o) => ({ outputId: o.outputId, error: o.error, transient: o.transient })),
        checks: req.checks.map((c) => ({ cardId: c.cardId, missing: c.missing, title: c.title, url: c.url })),
      }),
    })),
    forgetDeviceConnection: guard(async (req, ctx: HandlerContext) => {
      await forgetDeviceConnection(await requirePrincipal(ctx), req.connectionId);
      return {};
    }),
  });
}
