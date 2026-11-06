/**
 * Second factor: TOTP with one-time recovery codes.
 *
 * Every session is created through Better Auth, so one database hook (better-auth.ts) decides for all ways of signing in:
 * a session of somebody who has a factor, or who must have one, starts as `mfa_pending` and is good for nothing but the
 * endpoints here and signing out, until POST /api/auth/mfa/verify (or enrollment on the sign-in page) clears it. Devices
 * are approved from a session, so they are covered as well. When the policy starts requiring a factor, devices of people
 * without one can no longer refresh their access tokens (devices.ts) and so have to sign in again, which enrolls them.
 */
import { randomBytes, timingSafeEqual } from "node:crypto";
import { and, eq, sql } from "drizzle-orm";
import { renderSVG } from "uqr";
import { record } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { config } from "../env.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";
import { ApiError } from "../lib/errors.ts";
import { sha256 } from "../lib/ids.ts";
import { normalizeRole, parseRole, type Role } from "./roles.ts";
import { newTotpSecret, otpauthUri, verifyTotp } from "./totp.ts";

const POLICY_KEY = "mfa_policy";
const RECOVERY_CODES = 10;

export interface MfaPolicy {
  required: boolean;
  /** Empty: every role. */
  roles: Role[];
  exempt_sso: boolean;
}

export async function getMfaPolicy(): Promise<MfaPolicy> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, POLICY_KEY) });
  const v = (r?.value ?? {}) as Partial<MfaPolicy>;
  return { required: !!v.required, roles: Array.isArray(v.roles) ? v.roles : [], exempt_sso: !!v.exempt_sso };
}

/** Whether the policy asks a factor of this role (coming in by SSO or not). */
export function policyCovers(p: MfaPolicy, role: string, viaSso: boolean): boolean {
  if (!p.required) return false;
  if (viaSso && p.exempt_sso) return false;
  return p.roles.length === 0 || p.roles.includes(normalizeRole(role));
}

/** Replaces the policy. Refused when it would lock the caller out of the very settings they are changing. */
export async function setMfaPolicy(input: { required: boolean; roles: string[]; exempt_sso: boolean }, caller: { id: string; role: string }): Promise<MfaPolicy> {
  const policy: MfaPolicy = { required: !!input.required, roles: [...new Set(input.roles.map(parseRole))], exempt_sso: !!input.exempt_sso };
  if (policyCovers(policy, caller.role, false) && !(await hasFactor(caller.id))) {
    throw new ApiError("failed_precondition", "Turn on two-factor sign-in for your own account first (Settings, Account)");
  }
  await ensureDb();
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key: POLICY_KEY, value: policy, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: policy, updatedAt: now } });
  return policy;
}

const secretKey = () => deriveKey("setting-secrets");
const secretAad = (userId: string) => `user_mfa:${userId}:secret`;

async function row(userId: string) {
  await ensureDb();
  return getDb().query.userMfa.findFirst({ where: eq(schema.userMfa.userId, userId) });
}

export async function hasFactor(userId: string): Promise<boolean> {
  return !!(await row(userId))?.enabledAt;
}

export interface MfaStatus {
  enabled: boolean;
  enabled_at: string;
  recovery_codes_left: number;
  required: boolean;
}

export async function mfaStatus(user: { id: string; role: string }): Promise<MfaStatus> {
  const r = await row(user.id);
  return {
    enabled: !!r?.enabledAt,
    enabled_at: r?.enabledAt?.toISOString() ?? "",
    recovery_codes_left: r?.enabledAt ? ((r.recoveryHashes as string[]) ?? []).length : 0,
    required: policyCovers(await getMfaPolicy(), user.role, false),
  };
}

export interface Enrollment {
  secret: string;
  uri: string;
  qr_svg: string;
}

/** A new unconfirmed secret. Refused while a confirmed one exists: that one has to be removed first, with a code. */
export async function startEnrollment(user: { id: string; email: string }): Promise<Enrollment> {
  const existing = await row(user.id);
  if (existing?.enabledAt) throw new ApiError("failed_precondition", "Two-factor sign-in is already on");
  const secret = newTotpSecret();
  const secretEnc = encryptSecret(secret, secretAad(user.id), secretKey());
  const now = new Date();
  await getDb()
    .insert(schema.userMfa)
    .values({ userId: user.id, secretEnc, createdAt: now, updatedAt: now })
    .onConflictDoUpdate({ target: schema.userMfa.userId, set: { secretEnc, lastStep: 0, recoveryHashes: [], updatedAt: now } });
  const uri = otpauthUri(new URL(config().publicUrl).hostname || "harness", user.email, secret);
  return { secret, uri, qr_svg: renderSVG(uri, { border: 2 }) };
}

function newRecoveryCodes(): { codes: string[]; hashes: string[] } {
  const codes = Array.from({ length: RECOVERY_CODES }, () => {
    const s = randomBytes(8).toString("hex");
    return `${s.slice(0, 8)}-${s.slice(8)}`;
  });
  return { codes, hashes: codes.map(hashRecovery) };
}

const hashRecovery = (code: string) => sha256(code.trim().toLowerCase());

/** Checks a code against the unconfirmed secret, turns the factor on and returns the recovery codes (shown once). */
export async function confirmEnrollment(userId: string, code: string): Promise<string[]> {
  const r = await row(userId);
  if (!r) throw new ApiError("failed_precondition", "Start enrollment first");
  if (r.enabledAt) throw new ApiError("failed_precondition", "Two-factor sign-in is already on");
  const step = verifyTotp(decryptSecret(r.secretEnc, secretAad(userId), secretKey()), code, Date.now(), 0);
  if (step === undefined) throw new ApiError("invalid_request", "That code is not right. Check the time on your device and try the next one.");
  const { codes, hashes } = newRecoveryCodes();
  const now = new Date();
  await getDb().update(schema.userMfa).set({ enabledAt: now, lastStep: step, recoveryHashes: hashes, updatedAt: now }).where(eq(schema.userMfa.userId, userId));
  await record({ action: "mfa.enable", actor: { id: userId }, target: { type: "user", id: userId } });
  return codes;
}

export type FactorResult = "totp" | "recovery" | undefined;

/**
 * Checks a TOTP or recovery code of a confirmed factor. Consumes it: the time step is recorded and a recovery code
 * removed with a compare-and-set, so of two concurrent uses one fails.
 */
export async function checkFactor(userId: string, code: string): Promise<FactorResult> {
  const r = await row(userId);
  if (!r?.enabledAt) return undefined;
  const db = getDb();
  const clean = code.trim();
  const m = schema.userMfa;
  if (/^\d[\d\s]*$/.test(clean)) {
    const step = verifyTotp(decryptSecret(r.secretEnc, secretAad(userId), secretKey()), clean, Date.now(), r.lastStep);
    if (step === undefined) return undefined;
    const done = await db.update(m).set({ lastStep: step, updatedAt: new Date() }).where(and(eq(m.userId, userId), eq(m.lastStep, r.lastStep))).returning({ userId: m.userId });
    return done.length ? "totp" : undefined;
  }
  const hash = Buffer.from(hashRecovery(clean));
  const hashes = (r.recoveryHashes as string[]) ?? [];
  const match = hashes.find((h) => {
    const b = Buffer.from(h);
    return b.length === hash.length && timingSafeEqual(b, hash);
  });
  if (!match) return undefined;
  const left = hashes.filter((h) => h !== match);
  const done = await db
    .update(m)
    .set({ recoveryHashes: left, updatedAt: new Date() })
    .where(and(eq(m.userId, userId), sql`${m.recoveryHashes} = ${JSON.stringify(hashes)}::jsonb`))
    .returning({ userId: m.userId });
  if (!done.length) return undefined;
  await record({ action: "mfa.recovery_code_used", actor: { id: userId }, target: { type: "user", id: userId }, details: { left: left.length } });
  return "recovery";
}

export async function disableMfa(user: { id: string; role: string }, code: string): Promise<void> {
  if (policyCovers(await getMfaPolicy(), user.role, false)) throw new ApiError("failed_precondition", "Your organization requires two-factor sign-in");
  if (!(await hasFactor(user.id))) return;
  if (!(await checkFactor(user.id, code))) throw new ApiError("invalid_request", "That code is not right");
  await getDb().delete(schema.userMfa).where(eq(schema.userMfa.userId, user.id));
}

export async function regenerateRecoveryCodes(userId: string, code: string): Promise<string[]> {
  if (!(await hasFactor(userId))) throw new ApiError("failed_precondition", "Two-factor sign-in is not on");
  if ((await checkFactor(userId, code)) !== "totp") throw new ApiError("invalid_request", "That code is not right");
  const { codes, hashes } = newRecoveryCodes();
  await getDb().update(schema.userMfa).set({ recoveryHashes: hashes, updatedAt: new Date() }).where(eq(schema.userMfa.userId, userId));
  return codes;
}

/** An admin removes somebody's factor (a lost phone). */
export async function resetUserMfa(userId: string): Promise<void> {
  await ensureDb();
  await getDb().delete(schema.userMfa).where(eq(schema.userMfa.userId, userId));
}

/** Whether a new session of this user owes a second factor (the hook in better-auth.ts). */
export async function sessionOwesFactor(userId: string, viaSso: boolean): Promise<boolean> {
  const policy = await getMfaPolicy();
  const u = await getDb().query.user.findFirst({ where: eq(schema.user.id, userId), columns: { role: true } });
  if (policyCovers(policy, u?.role ?? "user", viaSso)) return true;
  if (viaSso && policy.exempt_sso) return false;
  return hasFactor(userId);
}

/** Whether this user's connected devices must stop until they enrolled (the policy requires a factor they do not have). */
export async function deviceBlockedByPolicy(user: { id: string; role: string | null }): Promise<boolean> {
  const policy = await getMfaPolicy();
  if (!policy.required) return false;
  if (await hasFactor(user.id)) return false;
  if (!policyCovers(policy, user.role ?? "user", false)) return false;
  if (policy.exempt_sso) {
    const accounts = await getDb().select({ providerId: schema.account.providerId }).from(schema.account).where(eq(schema.account.userId, user.id));
    if (accounts.some((a) => a.providerId.startsWith("sso:"))) return false;
  }
  return true;
}

/** Marks a session as past the second factor. */
export async function clearPending(sessionId: string): Promise<void> {
  await getDb().update(schema.session).set({ mfaPending: false, updatedAt: new Date() }).where(eq(schema.session.id, sessionId));
}
