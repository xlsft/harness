/** Maps service errors (ApiError) to Connect errors with a harness.common.v1.ErrorInfo detail. */
import { Code, ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema, ErrorType } from "../gen/harness/common/v1/errors_pb.ts";
import { ApiError, asApiError } from "../lib/errors.ts";

const MAP: Record<string, [ErrorType, Code]> = {
  invalid_request: [ErrorType.INVALID_ARGUMENT, Code.InvalidArgument],
  authentication: [ErrorType.UNAUTHENTICATED, Code.Unauthenticated],
  permission: [ErrorType.PERMISSION_DENIED, Code.PermissionDenied],
  not_found: [ErrorType.NOT_FOUND, Code.NotFound],
  rate_limit: [ErrorType.RATE_LIMITED, Code.ResourceExhausted],
  quota_exceeded: [ErrorType.QUOTA_EXCEEDED, Code.ResourceExhausted],
  overloaded: [ErrorType.OVERLOADED, Code.Unavailable],
  upstream: [ErrorType.UPSTREAM, Code.Unavailable],
  unsupported_client: [ErrorType.UNSUPPORTED_CLIENT, Code.FailedPrecondition],
  protocol_version: [ErrorType.UNSUPPORTED_CLIENT, Code.FailedPrecondition],
  internal: [ErrorType.INTERNAL, Code.Internal],
  conflict: [ErrorType.CONFLICT, Code.Aborted],
};

/** Connect code name ("invalid_argument", "permission_denied", ...) for a service error type. */
export function errorCode(type: string): string {
  const code = MAP[type]?.[1] ?? Code.Internal;
  return Code[code].replace(/[A-Z]/g, (c, i) => (i ? "_" : "") + c.toLowerCase());
}

export function errorType(type: string): ErrorType {
  return MAP[type]?.[0] ?? ErrorType.INTERNAL;
}

export function toConnectError(err: unknown): ConnectError {
  if (err instanceof ConnectError) return err;
  const e = asApiError(err);
  return connectErrorFrom(e.type, e.message, { retryAfterMs: e.retryAfterMs, upstreamStatus: e.upstreamStatus });
}

export function connectErrorFrom(
  type: string,
  message: string,
  opts: { retryAfterMs?: number; upstreamStatus?: number } = {},
): ConnectError {
  const [et, code] = MAP[type] ?? [ErrorType.INTERNAL, Code.Internal];
  return new ConnectError(message, code, undefined, [
    {
      desc: ErrorInfoSchema,
      value: {
        type: et,
        retryAfterMs: BigInt(Math.max(0, Math.round(opts.retryAfterMs ?? 0))),
        upstreamStatus: opts.upstreamStatus ?? 0,
      },
    },
  ]);
}

/** Wraps a unary implementation so ApiErrors become Connect errors. */
export function guard<A extends unknown[], R>(fn: (...args: A) => Promise<R>): (...args: A) => Promise<R> {
  return async (...args: A) => {
    try {
      return await fn(...args);
    } catch (err) {
      throw toConnectError(err);
    }
  };
}

export { ApiError };
