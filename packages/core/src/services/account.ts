/**
 * The signed-in user's own account (DeviceAuthService GetMe / UpdateMe / ChangePassword): profile, display
 * name, avatar and local password. Transport-agnostic. Writes go through Better Auth's internal adapter and
 * password hasher, the same primitives its /change-password endpoint uses (that endpoint needs a cookie
 * session, while the apps call with device access tokens).
 *
 * The avatar is Better Auth's `user.image`, stored as a small data URL the client has already resized, so it
 * is returned inline with the profile and needs no separate file serving.
 */
import { and, eq, max, ne } from "drizzle-orm";
import { getAuth } from "../auth/better-auth.ts";
import { LDAP_PROVIDER_ID } from "../auth/ldap-sign-in.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { TokenBucketLimiter } from "../lib/rate-limit.ts";
import { revokeDevice, toPublicUser, type Principal, type PublicUser } from "./devices.ts";

export interface Profile extends PublicUser {
  username: string;
  /** Data URL; "" when none is set. */
  image: string;
  source: "local" | "ldap";
  created_at: string;
  /** Latest web sign-in; "" when never. */
  last_sign_in_at: string;
}

export const AVATAR_MAX_BYTES = 200 * 1024;
export const AVATAR_MAX_SIDE = 256;
const NAME_MAX = 100;
const MIN_PASSWORD = 8;
const MAX_PASSWORD = 128;

type UserRow = typeof schema.user.$inferSelect;

async function findSelf(p: Principal): Promise<UserRow> {
  await ensureDb();
  const u = await getDb().query.user.findFirst({ where: eq(schema.user.id, p.user.id) });
  if (!u) throw new ApiError("authentication", "User not found");
  return u;
}

async function isLdapUser(userId: string): Promise<boolean> {
  const row = await getDb().query.account.findFirst({
    where: and(eq(schema.account.userId, userId), eq(schema.account.providerId, LDAP_PROVIDER_ID)),
  });
  return !!row;
}

async function profileOf(u: UserRow): Promise<Profile> {
  const [s] = await getDb()
    .select({ signIn: max(schema.session.createdAt) })
    .from(schema.session)
    .where(eq(schema.session.userId, u.id));
  return {
    ...toPublicUser(u),
    username: u.username ?? "",
    image: u.image ?? "",
    source: (await isLdapUser(u.id)) ? "ldap" : "local",
    created_at: u.createdAt.toISOString(),
    last_sign_in_at: s?.signIn ? s.signIn.toISOString() : "",
  };
}

export async function getProfile(p: Principal): Promise<Profile> {
  return profileOf(await findSelf(p));
}

export interface ProfilePatch {
  /** Undefined: unchanged. */
  name?: string;
  /** Undefined: unchanged; "": removes the avatar. */
  image?: string;
}

export async function updateProfile(p: Principal, patch: ProfilePatch): Promise<Profile> {
  const u = await findSelf(p);
  const set: Partial<typeof schema.user.$inferInsert> = {};
  if (patch.name !== undefined) {
    const name = patch.name.trim();
    if (!name) throw new ApiError("invalid_request", "Name is required");
    if (name.length > NAME_MAX) throw new ApiError("invalid_request", `Names have at most ${NAME_MAX} characters`);
    if (name !== u.name) {
      if (await isLdapUser(u.id)) throw new ApiError("failed_precondition", "Your name comes from the directory (LDAP) and is updated at every sign-in");
      set.name = name;
    }
  }
  if (patch.image !== undefined) {
    const image = patch.image === "" ? null : parseAvatar(patch.image);
    if (image !== u.image) set.image = image;
  }
  if (!Object.keys(set).length) return profileOf(u);
  const ctx = await getAuth().$context;
  await ctx.internalAdapter.updateUser(u.id, { ...set, updatedAt: new Date() });
  return getProfile(p);
}

// --- avatar ---------------------------------------------------------------------------------------------

type ImageType = "png" | "jpeg" | "webp";

/**
 * Validates an avatar data URL ("data:image/<png|jpeg|webp>;base64,...") and returns it normalized. The
 * content must be of the declared type (checked by its signature), at most AVATAR_MAX_BYTES decoded and
 * AVATAR_MAX_SIDE pixels on each side.
 */
export function parseAvatar(dataUrl: string): string {
  const m = /^data:image\/(png|jpeg|jpg|webp);base64,([A-Za-z0-9+/=\s]+)$/i.exec(dataUrl);
  if (!m) throw new ApiError("invalid_request", "The avatar must be a PNG, JPEG or WebP image (a base64 data URL)");
  const declared = (m[1]!.toLowerCase() === "jpg" ? "jpeg" : m[1]!.toLowerCase()) as ImageType;
  // Rough bound before decoding: base64 is 4/3 of the bytes.
  if (m[2]!.length > Math.ceil((AVATAR_MAX_BYTES * 4) / 3) + 64) throw tooBig();
  const bytes = Buffer.from(m[2]!.replace(/\s+/g, ""), "base64");
  if (bytes.length > AVATAR_MAX_BYTES) throw tooBig();
  const size = imageSize(bytes);
  if (!size || size.type !== declared) throw new ApiError("invalid_request", `The avatar is not a valid ${declared.toUpperCase()} image`);
  if (size.width < 1 || size.height < 1 || size.width > AVATAR_MAX_SIDE || size.height > AVATAR_MAX_SIDE) {
    throw new ApiError("invalid_request", `The avatar must be at most ${AVATAR_MAX_SIDE}x${AVATAR_MAX_SIDE} pixels (got ${size.width}x${size.height})`);
  }
  return `data:image/${declared};base64,${bytes.toString("base64")}`;
}

function tooBig(): ApiError {
  return new ApiError("invalid_request", `The avatar must be at most ${AVATAR_MAX_BYTES / 1024} KB`);
}

/** Type and pixel size from the image header; undefined when it is not a PNG, JPEG or WebP. */
export function imageSize(b: Buffer): { type: ImageType; width: number; height: number } | undefined {
  // PNG: signature, then the IHDR chunk with width and height.
  if (b.length >= 24 && b.readUInt32BE(0) === 0x89504e47 && b.readUInt32BE(4) === 0x0d0a1a0a && b.toString("ascii", 12, 16) === "IHDR") {
    return { type: "png", width: b.readUInt32BE(16), height: b.readUInt32BE(20) };
  }
  // JPEG: walk the segments up to a start-of-frame marker.
  if (b.length >= 4 && b[0] === 0xff && b[1] === 0xd8) {
    let i = 2;
    while (i + 9 < b.length) {
      if (b[i] !== 0xff) return undefined;
      const marker = b[i + 1]!;
      if (marker === 0xff) {
        i++;
        continue;
      }
      const len = b.readUInt16BE(i + 2);
      const sof = marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc;
      if (sof) return { type: "jpeg", height: b.readUInt16BE(i + 5), width: b.readUInt16BE(i + 7) };
      if (len < 2) return undefined;
      i += 2 + len;
    }
    return undefined;
  }
  // WebP: RIFF container with a VP8 (lossy), VP8L (lossless) or VP8X (extended) first chunk.
  if (b.length >= 30 && b.toString("ascii", 0, 4) === "RIFF" && b.toString("ascii", 8, 12) === "WEBP") {
    const chunk = b.toString("ascii", 12, 16);
    if (chunk === "VP8 " && b[23] === 0x9d && b[24] === 0x01 && b[25] === 0x2a) {
      return { type: "webp", width: b.readUInt16LE(26) & 0x3fff, height: b.readUInt16LE(28) & 0x3fff };
    }
    if (chunk === "VP8L" && b[20] === 0x2f) {
      const bits = b.readUInt32LE(21);
      return { type: "webp", width: (bits & 0x3fff) + 1, height: ((bits >> 14) & 0x3fff) + 1 };
    }
    if (chunk === "VP8X") {
      return { type: "webp", width: b.readUIntLE(24, 3) + 1, height: b.readUIntLE(27, 3) + 1 };
    }
  }
  return undefined;
}

// --- password -------------------------------------------------------------------------------------------

/** 10 attempts per user, refilled over 15 minutes. In memory, per process. */
const passwordAttempts = new TokenBucketLimiter(10, 10 / 900);

/** For tests. */
export function resetPasswordAttempts(): void {
  passwordAttempts.reset();
}

export async function changePassword(p: Principal, input: { current_password: string; new_password: string; revoke_other_sessions?: boolean }): Promise<void> {
  const u = await findSelf(p);
  if (await isLdapUser(u.id)) {
    throw new ApiError("failed_precondition", "You sign in through the directory (LDAP); change your password there");
  }
  const wait = passwordAttempts.take(u.id);
  if (wait > 0) throw new ApiError("rate_limit", "Too many password change attempts; try again later", { retryAfterMs: wait });
  const next = input.new_password;
  if (next.length < MIN_PASSWORD) throw new ApiError("invalid_request", `Passwords have at least ${MIN_PASSWORD} characters`);
  if (next.length > MAX_PASSWORD) throw new ApiError("invalid_request", `Passwords have at most ${MAX_PASSWORD} characters`);

  const ctx = await getAuth().$context;
  const account = await ctx.internalAdapter.findCredentialAccount(u.id);
  if (!account?.password) throw new ApiError("failed_precondition", "Your account has no local password; ask an administrator to set one");
  if (!input.current_password || !(await ctx.password.verify({ hash: account.password, password: input.current_password }))) {
    throw new ApiError("invalid_request", "The current password is incorrect");
  }
  await ctx.internalAdapter.updatePassword(u.id, await ctx.password.hash(next));

  if (input.revoke_other_sessions) {
    await ctx.internalAdapter.deleteUserSessions(u.id);
    const db = getDb();
    const others = await db.query.device.findMany({
      where: p.deviceId ? and(eq(schema.device.userId, u.id), ne(schema.device.id, p.deviceId)) : eq(schema.device.userId, u.id),
    });
    for (const d of others) if (!d.revokedAt) await revokeDevice(d.id);
  }
}
