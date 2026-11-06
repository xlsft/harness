/**
 * Better Auth plugin endpoints the sign-in page uses while a session owes its second factor:
 *   GET  /api/auth/mfa/state            what is owed: a code, or enrollment first
 *   POST /api/auth/mfa/verify           {code}: a TOTP or recovery code clears the session
 *   POST /api/auth/mfa/enroll/start     a new secret, for somebody who has none and must have one
 *   POST /api/auth/mfa/enroll/confirm   {code}: turns it on, clears the session, returns the recovery codes once
 * Failures count towards the same limit as wrong passwords, per user.
 */
import type { BetterAuthPlugin } from "better-auth";
import { APIError, createAuthEndpoint, getSessionFromCtx } from "better-auth/api";
import * as z from "zod";
import { record } from "../audit/log.ts";
import { ApiError } from "../lib/errors.ts";
import { AUTH_PEER_IP_HEADER, loginLimit, loginLimiter, tooManyAttempts } from "./ldap-sign-in.ts";
import { checkFactor, clearPending, confirmEnrollment, hasFactor, startEnrollment } from "./mfa.ts";

const codeBody = z.object({ code: z.string().min(1).max(64) });

type Ctx = Parameters<typeof getSessionFromCtx>[0];

async function pendingSession(ctx: Ctx) {
  const s = await getSessionFromCtx(ctx);
  if (!s) throw new APIError("UNAUTHORIZED", { message: "Sign in first" });
  return s as typeof s & { session: { mfaPending?: boolean } };
}

function enter(userId: string): string {
  const key = `mfa:${userId}`;
  const wait = loginLimiter.retryAfter(key, loginLimit());
  if (wait > 0) throw tooManyAttempts(wait);
  return key;
}

function apiError(e: unknown): never {
  if (e instanceof ApiError) {
    throw new APIError(e.type === "failed_precondition" ? "CONFLICT" : "BAD_REQUEST", { message: e.message });
  }
  throw e;
}

export function mfaEndpoints(): BetterAuthPlugin {
  return {
    id: "harness-mfa",
    endpoints: {
      mfaState: createAuthEndpoint("/mfa/state", { method: "GET" }, async (ctx) => {
        const s = await pendingSession(ctx as unknown as Ctx);
        return ctx.json({ pending: !!s.session.mfaPending, enrolled: await hasFactor(s.user.id) });
      }),

      mfaVerify: createAuthEndpoint("/mfa/verify", { method: "POST", body: codeBody }, async (ctx) => {
        const s = await pendingSession(ctx as unknown as Ctx);
        const key = enter(s.user.id);
        const ip = ctx.request?.headers.get(AUTH_PEER_IP_HEADER) ?? "";
        const used = await checkFactor(s.user.id, ctx.body.code);
        if (!used) {
          loginLimiter.fail(key);
          await record({ action: "auth.mfa_failed", outcome: "failure", actor: { id: s.user.id, label: s.user.email, ip } });
          throw new APIError("UNAUTHORIZED", { message: "That code is not right" });
        }
        await clearPending(s.session.id);
        await record({ action: "auth.mfa_verified", actor: { id: s.user.id, label: s.user.email, ip }, details: { factor: used } });
        return ctx.json({ ok: true });
      }),

      mfaEnrollStart: createAuthEndpoint("/mfa/enroll/start", { method: "POST" }, async (ctx) => {
        const s = await pendingSession(ctx as unknown as Ctx);
        if (!s.session.mfaPending) throw new APIError("CONFLICT", { message: "Enroll from Settings, Account" });
        try {
          return ctx.json(await startEnrollment({ id: s.user.id, email: s.user.email }));
        } catch (e) {
          apiError(e);
        }
      }),

      mfaEnrollConfirm: createAuthEndpoint("/mfa/enroll/confirm", { method: "POST", body: codeBody }, async (ctx) => {
        const s = await pendingSession(ctx as unknown as Ctx);
        if (!s.session.mfaPending) throw new APIError("CONFLICT", { message: "Enroll from Settings, Account" });
        const key = enter(s.user.id);
        let codes: string[];
        try {
          codes = await confirmEnrollment(s.user.id, ctx.body.code);
        } catch (e) {
          loginLimiter.fail(key);
          apiError(e);
        }
        await clearPending(s.session.id);
        return ctx.json({ recovery_codes: codes });
      }),
    },
  };
}
