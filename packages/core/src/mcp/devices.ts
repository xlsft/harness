/**
 * stdio MCP servers on devices: per-device availability / approval reports and IssueStdioLaunch, the only
 * place that hands out secret values (to a device token, for an approved config hash, short-lived).
 */
import { and, eq, inArray, isNull } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApprovalState, Availability } from "../gen/harness/mcp/v1/mcp_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { TokenBucketLimiter } from "../lib/rate-limit.ts";
import type { Principal } from "../services/devices.ts";
import { audit } from "./audit.ts";
import { secretRefs, substitute, substituteMap } from "./config.ts";
import type { ToolData } from "./proxy.ts";
import { getServerRow, isStdio, loadSecrets, rowConfig, visibleRows } from "./registry.ts";

export const LAUNCH_TTL_MS = 60_000;
const MAX_STATUSES = 500;
const MAX_TOOLS = 1000;
const MAX_TOOLS_BYTES = 2 << 20;

export interface DeviceStatusInput {
  serverId: string;
  configHash: string;
  availability: Availability;
  reason: string;
  approval: ApprovalState;
  lastError: string;
  tools: ToolData[];
}

export type DeviceStatusRow = typeof schema.mcpDeviceStatus.$inferSelect;

function requireDevice(p: Principal): string {
  if (p.via !== "access_token" || !p.deviceId) throw new ApiError("permission", "Only a device can do this (device token required)");
  return p.deviceId;
}

function clip(s: string, n: number): string {
  return (s ?? "").slice(0, n);
}

export async function reportDeviceStatus(p: Principal, statuses: DeviceStatusInput[]): Promise<void> {
  const deviceId = requireDevice(p);
  if (statuses.length > MAX_STATUSES) throw new ApiError("invalid_request", `At most ${MAX_STATUSES} statuses per report`);
  await ensureDb();
  const ids = [...new Set(statuses.map((s) => s.serverId))];
  if (ids.some((id) => !id)) throw new ApiError("invalid_request", "server_id is required");
  if (ids.length !== statuses.length) throw new ApiError("invalid_request", "Duplicate server_id in report");
  const visible = new Set((await visibleRows(p.user, { includeDisabled: true })).map((r) => r.id));
  const unknown = ids.filter((id) => !visible.has(id));
  if (unknown.length) throw new ApiError("not_found", `MCP server not found: ${unknown.join(", ")}`);
  const now = new Date();
  for (const s of statuses) {
    if (s.tools.length > MAX_TOOLS || JSON.stringify(s.tools).length > MAX_TOOLS_BYTES) {
      throw new ApiError("invalid_request", `Too many tools reported for ${s.serverId}`);
    }
    const values = {
      userId: p.user.id,
      configHash: clip(s.configHash, 64),
      availability: s.availability,
      reason: clip(s.reason, 1000),
      approval: s.approval,
      lastError: clip(s.lastError, 2000),
      reportedAt: now,
      // An empty list means "nothing discovered in this report": keep the last known tools.
      ...(s.tools.length ? { tools: s.tools, toolsReportedAt: now } : {}),
    };
    await getDb()
      .insert(schema.mcpDeviceStatus)
      .values({ deviceId, serverId: s.serverId, ...values })
      .onConflictDoUpdate({ target: [schema.mcpDeviceStatus.deviceId, schema.mcpDeviceStatus.serverId], set: values });
  }
}

export interface DeviceStatusEntry {
  deviceId: string;
  deviceName: string;
  status: DeviceStatusRow;
}

/** Status rows of the caller's own (non-revoked) devices for servers it can see. */
export async function listDeviceStatus(p: Principal, serverId: string, deviceId: string): Promise<DeviceStatusEntry[]> {
  await ensureDb();
  if (serverId) await getServerRow(p.user, serverId);
  const ds = schema.mcpDeviceStatus;
  const rows = await getDb()
    .select({ status: ds, deviceName: schema.device.name })
    .from(ds)
    .innerJoin(schema.device, eq(schema.device.id, ds.deviceId))
    .where(
      and(
        eq(schema.device.userId, p.user.id),
        isNull(schema.device.revokedAt),
        serverId ? eq(ds.serverId, serverId) : undefined,
        deviceId ? eq(ds.deviceId, deviceId) : undefined,
      ),
    );
  const visible = new Set((await visibleRows(p.user, { includeDisabled: true })).map((r) => r.id));
  return rows.filter((r) => visible.has(r.status.serverId)).map((r) => ({ deviceId: r.status.deviceId, deviceName: r.deviceName, status: r.status }));
}

/** Latest tool list reported by any of the user's devices per stdio server (for the catalog). */
export async function reportedTools(userId: string, serverIds: string[]): Promise<Map<string, { tools: ToolData[]; at: Date; configHash: string }[]>> {
  const out = new Map<string, { tools: ToolData[]; at: Date; configHash: string }[]>();
  if (!serverIds.length) return out;
  const ds = schema.mcpDeviceStatus;
  const rows = await getDb()
    .select({ serverId: ds.serverId, tools: ds.tools, at: ds.toolsReportedAt, configHash: ds.configHash })
    .from(ds)
    .innerJoin(schema.device, eq(schema.device.id, ds.deviceId))
    .where(and(eq(ds.userId, userId), isNull(schema.device.revokedAt), inArray(ds.serverId, serverIds)));
  for (const r of rows) {
    if (!r.at || !Array.isArray(r.tools) || !r.tools.length) continue;
    const list = out.get(r.serverId) ?? [];
    list.push({ tools: r.tools as ToolData[], at: r.at, configHash: r.configHash });
    out.set(r.serverId, list);
  }
  return out;
}

// --- IssueStdioLaunch ------------------------------------------------------------------------

let limiter: TokenBucketLimiter | undefined;

/** Per-device limit: HARNESS_MCP_LAUNCH_RATE_PER_MIN (default 30) burst and sustained per minute. */
function takeLaunchToken(deviceId: string): void {
  const n = Number(process.env.HARNESS_MCP_LAUNCH_RATE_PER_MIN ?? "");
  const perMin = Number.isFinite(n) && n > 0 ? n : 30;
  if (!limiter || limiter.capacity !== perMin) limiter = new TokenBucketLimiter(perMin, perMin / 60);
  const wait = limiter.take(deviceId);
  if (wait > 0) throw new ApiError("rate_limit", "Too many stdio launches from this device; slow down", { retryAfterMs: wait });
}

export interface StdioLaunch {
  command: string;
  args: string[];
  env: Record<string, string>;
  cwd: string;
  expiresAt: string;
}

export async function issueStdioLaunch(p: Principal, serverId: string, hash: string): Promise<StdioLaunch> {
  const deviceId = requireDevice(p);
  if (!hash) throw new ApiError("invalid_request", "config_hash is required");
  const row = await getServerRow(p.user, serverId);
  if (!isStdio(row)) throw new ApiError("invalid_request", "IssueStdioLaunch applies to stdio servers only");
  if (!row.enabled) throw new ApiError("failed_precondition", `MCP server ${row.name} is disabled`);
  if (row.configHash !== hash) {
    throw new ApiError("failed_precondition", "The server config changed since it was approved on this device; ask the user to approve the new config");
  }
  const st = await getDb().query.mcpDeviceStatus.findFirst({
    where: and(eq(schema.mcpDeviceStatus.deviceId, deviceId), eq(schema.mcpDeviceStatus.serverId, row.id)),
  });
  if (!st || st.approval !== ApprovalState.APPROVED || st.configHash !== hash) {
    throw new ApiError("failed_precondition", "This device has not reported an approval for this config hash (ReportDeviceStatus with APPROVED first)");
  }
  takeLaunchToken(deviceId);

  const device = await getDb().query.device.findFirst({ where: eq(schema.device.id, deviceId) });
  const cfg = rowConfig(row);
  const base = cfg.stdio!;
  let command = base.command;
  let args = base.args;
  let env = { ...base.env };
  // Overrides apply in order when every field they set matches this device; later ones win.
  for (const o of cfg.overrides) {
    if (o.deviceId && o.deviceId !== deviceId) continue;
    if (o.os && o.os.toLowerCase() !== (device?.os ?? "").toLowerCase()) continue;
    if (o.command) command = o.command;
    if (o.args.length) args = o.args;
    env = { ...env, ...o.env };
  }
  const secrets = await loadSecrets(row.id);
  const resolved = {
    command: substitute(command, secrets),
    args: args.map((a) => substitute(a, secrets)),
    env: substituteMap(env, secrets),
    cwd: substitute(base.cwd, secrets),
  };
  // Refuse to hand out values that could not be recorded.
  await audit(
    {
      userId: p.user.id,
      deviceId,
      serverId: row.id,
      action: "stdio_launch",
      configHash: hash,
      detail: { secrets: secretRefs(cfg) },
    },
    true,
  );
  return { ...resolved, expiresAt: new Date(Date.now() + LAUNCH_TTL_MS).toISOString() };
}
