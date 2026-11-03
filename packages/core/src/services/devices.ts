/**
 * Device flow and device/access tokens (docs/protocol/auth.md). Transport-agnostic:
 * every function takes plain values and returns plain objects or throws ApiError.
 */
import { and, eq, gt, isNull, lt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { ApiError } from "../lib/errors.ts";
import { newId, newToken, newUserCode, normalizeUserCode, sha256 } from "../lib/ids.ts";

export const DEVICE_CODE_TTL_SEC = 600;
export const POLL_INTERVAL_SEC = 5;
export const ACCESS_TOKEN_TTL_SEC = 3600;

export interface PublicUser {
  id: string;
  email: string;
  name: string;
  role: "admin" | "user";
}

export interface Principal {
  user: PublicUser;
  /** Set when authenticated with an access token. */
  deviceId?: string;
  via: "access_token" | "session";
}

export function toPublicUser(u: { id: string; email: string; name: string; role: string | null }): PublicUser {
  return { id: u.id, email: u.email, name: u.name, role: u.role === "admin" ? "admin" : "user" };
}

export type PollStatus = "pending" | "slow_down" | "approved" | "denied" | "expired";

export interface DeviceCredentials {
  device_id: string;
  device_token: string;
  access_token: string;
  access_expires_in: number;
  user: PublicUser;
}

export type PollResult = { status: Exclude<PollStatus, "approved"> } | { status: "approved"; credentials: DeviceCredentials };

// --- 1. POST /api/auth/device/code -------------------------------------------------

export interface DeviceCodeRequest {
  client_version?: string;
  client_name?: string;
  device_name?: string;
  os?: string;
  arch?: string;
}

export async function startDeviceAuthorization(req: DeviceCodeRequest) {
  await ensureDb();
  const db = getDb();
  const now = Date.now();
  // Opportunistic cleanup of stale requests.
  await db.delete(schema.deviceCode).where(lt(schema.deviceCode.expiresAt, new Date(now - 3600_000)));

  const deviceCode = newToken("hdc");
  let userCode = newUserCode();
  for (let i = 0; i < 5; i++) {
    const clash = await db.query.deviceCode.findFirst({ where: eq(schema.deviceCode.userCode, userCode) });
    if (!clash) break;
    userCode = newUserCode();
  }
  await db.insert(schema.deviceCode).values({
    deviceCodeHash: sha256(deviceCode),
    userCode,
    clientName: clip(req.client_name, 100),
    deviceName: clip(req.device_name, 200) ?? "unknown device",
    os: clip(req.os, 50),
    arch: clip(req.arch, 50),
    status: "pending",
    intervalSec: POLL_INTERVAL_SEC,
    expiresAt: new Date(now + DEVICE_CODE_TTL_SEC * 1000),
    createdAt: new Date(now),
  });
  const base = config().publicUrl;
  return {
    device_code: deviceCode,
    user_code: userCode,
    verification_uri: `${base}/device`,
    verification_uri_complete: `${base}/device?code=${encodeURIComponent(userCode)}`,
    expires_in: DEVICE_CODE_TTL_SEC,
    interval: POLL_INTERVAL_SEC,
  };
}

// --- 2. Browser approval (/device page) ---------------------------------------------

export async function findPendingByUserCode(userCode: string) {
  await ensureDb();
  const row = await getDb().query.deviceCode.findFirst({
    where: eq(schema.deviceCode.userCode, normalizeUserCode(userCode)),
  });
  if (!row || row.status !== "pending" || row.expiresAt.getTime() < Date.now()) return undefined;
  return row;
}

export async function decideDeviceAuthorization(userCode: string, userId: string, approve: boolean): Promise<boolean> {
  const row = await findPendingByUserCode(userCode);
  if (!row) return false;
  await getDb()
    .update(schema.deviceCode)
    .set({ status: approve ? "approved" : "denied", userId })
    .where(and(eq(schema.deviceCode.deviceCodeHash, row.deviceCodeHash), eq(schema.deviceCode.status, "pending")));
  return true;
}

// --- 3. POST /api/auth/device/token --------------------------------------------------

export async function pollDeviceToken(deviceCode: string | undefined, userAgent?: string): Promise<PollResult> {
  if (!deviceCode || typeof deviceCode !== "string") throw new ApiError("invalid_request", "device_code is required");
  await ensureDb();
  const db = getDb();
  const hash = sha256(deviceCode);
  const row = await db.query.deviceCode.findFirst({ where: eq(schema.deviceCode.deviceCodeHash, hash) });
  const now = Date.now();
  if (!row || row.status === "consumed" || row.expiresAt.getTime() < now) return { status: "expired" };
  if (row.status === "denied") return { status: "denied" };

  if (row.status === "pending") {
    const tooFast = row.lastPolledAt && now - row.lastPolledAt.getTime() < row.intervalSec * 1000;
    const intervalSec = tooFast ? row.intervalSec + 5 : row.intervalSec;
    await db
      .update(schema.deviceCode)
      .set({ lastPolledAt: new Date(now), intervalSec })
      .where(eq(schema.deviceCode.deviceCodeHash, hash));
    return { status: tooFast ? "slow_down" : "pending" };
  }

  // approved -> consume exactly once
  const consumed = await db
    .update(schema.deviceCode)
    .set({ status: "consumed" })
    .where(and(eq(schema.deviceCode.deviceCodeHash, hash), eq(schema.deviceCode.status, "approved")))
    .returning();
  if (consumed.length === 0 || !row.userId) return { status: "expired" };

  const u = await db.query.user.findFirst({ where: eq(schema.user.id, row.userId) });
  if (!u || u.banned) return { status: "denied" };

  const deviceToken = newToken("hdt");
  const deviceId = newId("dev");
  await db.insert(schema.device).values({
    id: deviceId,
    userId: u.id,
    name: row.deviceName ?? "unknown device",
    clientName: row.clientName,
    os: row.os,
    arch: row.arch,
    userAgent: clip(userAgent, 300),
    tokenHash: sha256(deviceToken),
    createdAt: new Date(now),
    lastSeenAt: new Date(now),
  });
  const access = await issueAccessToken(deviceId, u.id);
  return {
    status: "approved",
    credentials: {
      device_id: deviceId,
      device_token: deviceToken,
      access_token: access.access_token,
      access_expires_in: access.access_expires_in,
      user: toPublicUser(u),
    },
  };
}

async function issueAccessToken(deviceId: string, userId: string) {
  const token = newToken("hat");
  const now = Date.now();
  const db = getDb();
  await db.delete(schema.accessToken).where(lt(schema.accessToken.expiresAt, new Date(now)));
  await db.insert(schema.accessToken).values({
    tokenHash: sha256(token),
    deviceId,
    userId,
    expiresAt: new Date(now + ACCESS_TOKEN_TTL_SEC * 1000),
    createdAt: new Date(now),
  });
  return { access_token: token, access_expires_in: ACCESS_TOKEN_TTL_SEC };
}

// --- POST /api/auth/device/refresh ---------------------------------------------------

export async function refreshAccessToken(deviceToken: string | undefined) {
  if (!deviceToken || typeof deviceToken !== "string") throw new ApiError("authentication", "device_token is required");
  await ensureDb();
  const db = getDb();
  const dev = await db.query.device.findFirst({ where: eq(schema.device.tokenHash, sha256(deviceToken)) });
  if (!dev || dev.revokedAt) throw new ApiError("authentication", "Device is revoked or unknown");
  const u = await db.query.user.findFirst({ where: eq(schema.user.id, dev.userId) });
  if (!u || u.banned) throw new ApiError("authentication", "User is disabled");
  await db.update(schema.device).set({ lastSeenAt: new Date() }).where(eq(schema.device.id, dev.id));
  return issueAccessToken(dev.id, dev.userId);
}

// --- Bearer authentication -----------------------------------------------------------

const lastSeenWrites = new Map<string, number>();

export async function authenticateAccessToken(token: string): Promise<Principal | undefined> {
  if (!token.startsWith("hat_")) return undefined;
  await ensureDb();
  const db = getDb();
  const now = Date.now();
  const [row] = await db
    .select({ device: schema.device, user: schema.user, expiresAt: schema.accessToken.expiresAt })
    .from(schema.accessToken)
    .innerJoin(schema.device, eq(schema.device.id, schema.accessToken.deviceId))
    .innerJoin(schema.user, eq(schema.user.id, schema.accessToken.userId))
    .where(
      and(
        eq(schema.accessToken.tokenHash, sha256(token)),
        gt(schema.accessToken.expiresAt, new Date(now)),
        isNull(schema.device.revokedAt),
      ),
    )
    .limit(1);
  if (!row || row.user.banned) return undefined;
  // Throttle last_seen_at writes to once a minute per device.
  if ((lastSeenWrites.get(row.device.id) ?? 0) < now - 60_000) {
    lastSeenWrites.set(row.device.id, now);
    await db.update(schema.device).set({ lastSeenAt: new Date(now) }).where(eq(schema.device.id, row.device.id));
  }
  return { user: toPublicUser(row.user), deviceId: row.device.id, via: "access_token" };
}

// --- GET /api/auth/me, POST /api/auth/device/logout -----------------------------------

export interface DeviceInfo {
  id: string;
  name: string;
  os: string;
  arch: string;
  client_name: string | null;
  created_at: string;
  last_seen_at: string | null;
  /** The device making the call. */
  current: boolean;
}

function deviceInfo(d: typeof schema.device.$inferSelect, currentId?: string): DeviceInfo {
  return {
    id: d.id,
    name: d.name,
    os: d.os ?? "",
    arch: d.arch ?? "",
    client_name: d.clientName ?? null,
    created_at: d.createdAt.toISOString(),
    last_seen_at: d.lastSeenAt ? d.lastSeenAt.toISOString() : null,
    current: !!currentId && d.id === currentId,
  };
}

export async function describePrincipal(p: Principal) {
  let info: DeviceInfo | null = null;
  if (p.deviceId) {
    const d = await getDb().query.device.findFirst({ where: eq(schema.device.id, p.deviceId) });
    if (d) info = deviceInfo(d, p.deviceId);
  }
  return { user: p.user, device: info };
}

/**
 * Active (not revoked) devices of `userId` (default: the caller), most recently seen first.
 * Only admins may list another user's devices.
 */
export async function listDevices(p: Principal, userId?: string): Promise<DeviceInfo[]> {
  const target = userId || p.user.id;
  if (target !== p.user.id && p.user.role !== "admin") {
    throw new ApiError("permission", "Only admins can list other users' devices");
  }
  await ensureDb();
  const rows = await getDb().query.device.findMany({
    where: and(eq(schema.device.userId, target), isNull(schema.device.revokedAt)),
  });
  const seen = (d: typeof schema.device.$inferSelect) => (d.lastSeenAt ?? d.createdAt).getTime();
  return rows.sort((a, b) => seen(b) - seen(a)).map((d) => deviceInfo(d, p.deviceId));
}

/**
 * Revokes a device of the caller (admins: any device). Unknown devices and devices of other
 * users (for non-admins) are NOT_FOUND, so device ids of other users do not leak. The calling device is
 * refused (FAILED_PRECONDITION): revoking it would cut off the very client the request came through, so it
 * signs out with Logout instead.
 */
export async function revokeDeviceFor(p: Principal, deviceId: string): Promise<void> {
  if (!deviceId) throw new ApiError("invalid_request", "device_id is required");
  await ensureDb();
  const d = await getDb().query.device.findFirst({ where: eq(schema.device.id, deviceId) });
  if (!d || (d.userId !== p.user.id && p.user.role !== "admin")) throw new ApiError("not_found", "Device not found");
  if (d.id === p.deviceId) throw new ApiError("failed_precondition", "This is the device you are using; sign out on it instead");
  if (!d.revokedAt) await revokeDevice(deviceId);
}

export async function revokeDevice(deviceId: string): Promise<void> {
  await ensureDb();
  const db = getDb();
  await db.update(schema.device).set({ revokedAt: new Date() }).where(eq(schema.device.id, deviceId));
  await db.delete(schema.accessToken).where(eq(schema.accessToken.deviceId, deviceId));
}

function clip(v: unknown, max: number): string | null {
  if (typeof v !== "string" || !v) return null;
  return v.slice(0, max);
}
