/**
 * The inbox fold (docs/design/coordinator.md): what one session needs from the user, from its SessionEvents. Pure; the
 * client folds its live sessions with the same rules (packages/client/server/inbox.go), and both are tested on the same
 * scenarios.
 */
import { create } from "@bufbuild/protobuf";
import { SessionStatus, TurnFinished_Result, type SessionEvent, type SessionInfo } from "../gen/harness/agent/v1/agent_pb.ts";
import {
  InboxAgentSchema,
  InboxGroup,
  InboxQuestionSchema,
  InboxRequest_Kind,
  InboxRequestSchema,
  type InboxItem,
} from "../gen/harness/agent/v1/inbox_pb.ts";
import { ProjectRunState } from "../gen/harness/agent/v1/project_runs_pb.ts";
import { SubagentState } from "../gen/harness/agent/v1/subagents_pb.ts";

/** Bounds of what an item keeps. */
export const SUMMARY_CHARS = 400;
export const REPORT_CHARS = 600;
export const MAX_REQUESTS = 20;
export const MAX_AGENTS = 50;

/** What the fold needs between events that is not part of the item. */
export interface FoldScratch {
  /** Text of the latest assistant message of the running turn. */
  lastText?: string;
  /** How the latest turn ended: "", "success", "failed", "interrupted". */
  result?: string;
}

export function cut(s: string, n: number): string {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n - 1) + "…" : t;
}

/** A one-line summary of a tool input: the command, path or pattern when there is one, else the JSON. */
export function inputSummary(json: string): string {
  try {
    const v = JSON.parse(json) as Record<string, unknown>;
    for (const k of ["command", "file_path", "path", "url", "pattern", "query", "prompt", "description"]) {
      if (typeof v[k] === "string" && v[k]) return cut(v[k] as string, SUMMARY_CHARS);
    }
  } catch {
    /* not JSON: shown as it is */
  }
  return cut(json, SUMMARY_CHARS);
}

function applyInfo(item: InboxItem, info: SessionInfo): void {
  if (info.title) item.title = info.title;
  if (info.cwd) item.cwd = info.cwd;
  if (info.repo) item.repo = info.repo;
  if (info.deviceId) item.deviceId = info.deviceId;
  if (info.deviceName) item.deviceName = info.deviceName;
  item.parentSessionId = info.parentSessionId;
  item.working = info.status === SessionStatus.WORKING;
}

/** The group the item belongs to now; UNSPECIFIED when it needs nothing. */
export function groupOf(item: InboxItem, scratch: FoldScratch): InboxGroup {
  if (item.requests.length) return InboxGroup.NEEDS_INPUT;
  const agentRunning = item.agents.some((a) => a.state === SubagentState.RUNNING || a.state === SubagentState.QUEUED);
  if (item.working || agentRunning) return InboxGroup.RUNNING;
  const run = item.projectRun?.state;
  if (run === ProjectRunState.FAILED) return InboxGroup.FAILED;
  if (scratch.result === "failed") return InboxGroup.FAILED;
  if (!scratch.result && item.agents.some((a) => a.state === SubagentState.FAILED)) return InboxGroup.FAILED;
  if (scratch.result === "success" && item.report) return InboxGroup.FINISHED;
  if (run === ProjectRunState.DONE) return InboxGroup.FINISHED;
  return InboxGroup.UNSPECIFIED;
}

function removeRequest(item: InboxItem, id: string): boolean {
  const n = item.requests.length;
  item.requests = item.requests.filter((r) => r.requestId !== id);
  return item.requests.length !== n;
}

function addRequest(item: InboxItem, r: Parameters<typeof create<typeof InboxRequestSchema>>[1]): void {
  const req = create(InboxRequestSchema, r);
  removeRequest(item, req.requestId);
  item.requests.push(req);
  if (item.requests.length > MAX_REQUESTS) item.requests.splice(0, item.requests.length - MAX_REQUESTS);
}

function textOf(ev: Extract<SessionEvent["event"], { case: "assistantMessage" }>["value"]): string {
  return ev.content
    .map((b) => (b.block.case === "text" ? b.block.value.text : ""))
    .filter(Boolean)
    .join("\n");
}

/**
 * Folds one event into the item. Returns whether the item changed in a way the inbox shows. `at` is the event's time
 * (RFC 3339); attention_at moves on requests, finished and failed turns, failed agents and paused runs.
 */
export function foldEvent(item: InboxItem, scratch: FoldScratch, ev: SessionEvent): boolean {
  const at = ev.timestamp || new Date().toISOString();
  const e = ev.event;
  const attention = () => (item.attentionAt = at);
  switch (e.case) {
    case "sessionUpdated":
      applyInfo(item, e.value);
      break;
    case "turnStarted":
      item.working = true;
      item.error = "";
      scratch.lastText = "";
      break;
    case "assistantMessage": {
      const t = textOf(e.value);
      if (t.trim()) scratch.lastText = t;
      return false;
    }
    case "turnFinished": {
      item.working = false;
      const u = e.value.usage;
      if (u) {
        item.inputTokens += u.inputTokens + u.cacheReadInputTokens + u.cacheCreationInputTokens;
        item.outputTokens += u.outputTokens;
      }
      item.costUsd += e.value.costUsd;
      switch (e.value.result) {
        case TurnFinished_Result.SUCCESS:
          scratch.result = "success";
          item.report = cut(scratch.lastText ?? "", REPORT_CHARS);
          item.error = "";
          if (item.report) attention();
          break;
        case TurnFinished_Result.INTERRUPTED:
          scratch.result = "interrupted";
          break;
        default:
          scratch.result = "failed";
          item.error = cut(e.value.errorMessage || "The turn failed", REPORT_CHARS);
          attention();
      }
      break;
    }
    case "permissionRequest": {
      const p = e.value;
      addRequest(item, {
        kind: InboxRequest_Kind.PERMISSION,
        requestId: p.requestId,
        toolName: p.toolName,
        summary: inputSummary(p.inputJson),
        suggestedRules: p.suggestedRules.slice(0, 5),
        oneTimeOnly: p.oneTimeOnly,
        agentId: p.agentId,
        agentName: p.agentName,
        createdAt: at,
      });
      attention();
      break;
    }
    case "permissionResolved":
      if (!removeRequest(item, e.value.requestId)) return false;
      break;
    case "question": {
      const q = e.value;
      addRequest(item, {
        kind: InboxRequest_Kind.QUESTION,
        requestId: q.requestId,
        questions: q.items.slice(0, 8).map((i) =>
          create(InboxQuestionSchema, {
            question: cut(i.question, SUMMARY_CHARS),
            header: cut(i.header, 60),
            options: i.options.slice(0, 12).map((o) => cut(o.label, 120)),
            multiSelect: i.multiSelect,
            type: i.type,
          }),
        ),
        createdAt: at,
      });
      attention();
      break;
    }
    case "questionResolved":
      if (!removeRequest(item, e.value.requestId)) return false;
      break;
    case "planProposed":
      addRequest(item, { kind: InboxRequest_Kind.PLAN, requestId: e.value.requestId, summary: cut(e.value.plan, SUMMARY_CHARS), createdAt: at });
      attention();
      break;
    case "planResolved":
      if (!removeRequest(item, e.value.requestId)) return false;
      break;
    case "subagentUpdated": {
      const a = e.value.agent;
      if (!a?.id) return false;
      const prev = item.agents.find((x) => x.id === a.id);
      const next = create(InboxAgentSchema, {
        id: a.id,
        name: cut(a.name, 120),
        state: a.state,
        report: cut(a.report, REPORT_CHARS),
        error: cut(a.error, REPORT_CHARS),
        currentTool: a.currentTool,
        updatedAt: a.finishedAt || a.startedAt || at,
      });
      if (prev && prev.state === next.state && prev.currentTool === next.currentTool && prev.report === next.report) return false;
      if (next.state === SubagentState.FAILED && prev?.state !== SubagentState.FAILED) attention();
      item.agents = [...item.agents.filter((x) => x.id !== a.id), next].slice(-MAX_AGENTS);
      break;
    }
    case "sessionEnded":
      item.working = false;
      item.open = false;
      // What waited can't be answered any more: the session asks again when it resumes.
      item.requests = [];
      item.agents = item.agents.map((a) =>
        a.state === SubagentState.RUNNING || a.state === SubagentState.QUEUED ? create(InboxAgentSchema, { ...a, state: SubagentState.INTERRUPTED }) : a,
      );
      break;
    case "projectRunUpdated": {
      const run = e.value.run;
      if (!run) return false;
      item.projectRun = run;
      const id = `run:${run.id}`;
      if (run.state === ProjectRunState.PAUSED) {
        if (!item.requests.some((r) => r.requestId === id)) {
          addRequest(item, { kind: InboxRequest_Kind.PROJECT_RUN, requestId: id, summary: cut(run.reason || "The run is paused", SUMMARY_CHARS), createdAt: at });
          attention();
        }
      } else {
        removeRequest(item, id);
      }
      if (run.state === ProjectRunState.DONE || run.state === ProjectRunState.FAILED) attention();
      break;
    }
    default:
      return false;
  }
  item.updatedAt = at;
  return true;
}
