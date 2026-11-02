/**
 * In-memory token-bucket rate limiter (per process). Used for the unauthenticated device-flow
 * endpoints, keyed by client IP. Not shared between replicas; good enough as an abuse brake.
 */
import { ApiError } from "./errors.ts";

interface Bucket {
  tokens: number;
  updatedAt: number;
}

export class TokenBucketLimiter {
  private buckets = new Map<string, Bucket>();

  /**
   * @param capacity burst size (max tokens)
   * @param refillPerSec tokens added per second
   */
  constructor(
    readonly capacity: number,
    readonly refillPerSec: number,
    private now: () => number = Date.now,
  ) {}

  /** Takes one token for `key`. Returns 0 when allowed, otherwise the ms until a token is available. */
  take(key: string): number {
    const now = this.now();
    let b = this.buckets.get(key);
    if (!b) {
      b = { tokens: this.capacity, updatedAt: now };
      this.buckets.set(key, b);
      if (this.buckets.size > 10_000) this.sweep(now);
    } else {
      b.tokens = Math.min(this.capacity, b.tokens + ((now - b.updatedAt) / 1000) * this.refillPerSec);
      b.updatedAt = now;
    }
    if (b.tokens >= 1) {
      b.tokens -= 1;
      return 0;
    }
    if (this.refillPerSec <= 0) return Number.POSITIVE_INFINITY;
    return Math.ceil(((1 - b.tokens) / this.refillPerSec) * 1000);
  }

  /** Drops buckets that are full again (idle long enough); they would be recreated identically. */
  private sweep(now: number): void {
    for (const [k, b] of this.buckets) {
      const tokens = b.tokens + ((now - b.updatedAt) / 1000) * this.refillPerSec;
      if (tokens >= this.capacity) this.buckets.delete(k);
    }
  }

  reset(): void {
    this.buckets.clear();
  }
}

const limiters = new Map<string, TokenBucketLimiter>();

/**
 * Per-IP limit for a named endpoint group. HARNESS_DEVICE_RATE_LIMIT_PER_MIN (default 30) is both
 * the burst and the sustained rate per minute; 0 disables limiting.
 */
export function enforceIpRateLimit(group: string, ip: string | undefined): void {
  const perMin = intEnv(process.env.HARNESS_DEVICE_RATE_LIMIT_PER_MIN, 30);
  if (perMin === 0) return;
  let l = limiters.get(group);
  if (!l || l.capacity !== perMin) {
    l = new TokenBucketLimiter(perMin, perMin / 60);
    limiters.set(group, l);
  }
  const wait = l.take(ip || "unknown");
  if (wait > 0) {
    throw new ApiError("rate_limit", "Too many requests from this address; slow down", { retryAfterMs: wait });
  }
}

/** For tests. */
export function resetRateLimits(): void {
  limiters.clear();
}

function intEnv(v: string | undefined, def: number): number {
  if (v === undefined || v === "") return def;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) && n >= 0 ? n : def;
}
