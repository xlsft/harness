/** Error types from docs/protocol/README.md. */
export type ApiErrorType =
  | "invalid_request"
  | "authentication"
  | "permission"
  | "not_found"
  | "rate_limit"
  | "quota_exceeded"
  | "overloaded"
  | "upstream"
  | "internal"
  | "protocol_version"
  | "unsupported_client"
  | "conflict"
  | "needs_auth"
  | "failed_precondition";

const STATUS: Record<ApiErrorType, number> = {
  invalid_request: 400,
  authentication: 401,
  permission: 403,
  not_found: 404,
  rate_limit: 429,
  quota_exceeded: 429,
  overloaded: 529,
  upstream: 502,
  internal: 500,
  protocol_version: 426,
  unsupported_client: 426,
  conflict: 409,
  needs_auth: 412,
  failed_precondition: 412,
};

/** Transport-agnostic service error. Mapped to Connect codes in src/rpc/errors.ts and to JSON in src/http/util.ts. */
export class ApiError extends Error {
  readonly type: string;
  readonly status: number;
  readonly retryAfterMs?: number;
  readonly extra?: Record<string, unknown>;
  /** Upstream provider HTTP status, when the error came from a provider. */
  readonly upstreamStatus?: number;

  constructor(
    type: ApiErrorType | (string & {}),
    message: string,
    opts: { status?: number; retryAfterMs?: number; upstreamStatus?: number; extra?: Record<string, unknown> } = {},
  ) {
    super(message);
    this.type = type;
    this.status = opts.status ?? STATUS[type as ApiErrorType] ?? 500;
    this.retryAfterMs = opts.retryAfterMs;
    this.upstreamStatus = opts.upstreamStatus;
    this.extra = opts.extra;
  }

  toJSON(): { error: Record<string, unknown> } {
    const error: Record<string, unknown> = { type: this.type };
    if (this.message) error.message = this.message;
    if (this.retryAfterMs !== undefined) error.retry_after_ms = this.retryAfterMs;
    if (this.extra) Object.assign(error, this.extra);
    return { error };
  }
}

export function asApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  console.error("[harness] internal error", err);
  return new ApiError("internal", "Internal server error");
}
