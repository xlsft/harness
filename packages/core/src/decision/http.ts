/**
 * `http` provider: an external policy webhook (OPA, a company service, ...). Core POSTs the request as JSON
 * and expects `{decision, reason, categories?, confidence?}` back:
 *
 *   POST <url>
 *   {session_id, tool_name, input_json, input, cwd, additional_directories, recent_user_messages,
 *    assistant_rationale, facts: {subcommands, write_paths, network_hosts, inside_git_repo, git_branch},
 *    user_id, user_role, device_id}
 *
 * The URL goes through the SSRF guard: private and loopback addresses need `allow_private_network`.
 */
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import { ProviderError, type DecideInput, type DecisionContext, type DecisionProvider, type ProviderDecision } from "./types.ts";
import { parseVerdict } from "./verdict.ts";

export const DEFAULT_HTTP_TIMEOUT_MS = 5_000;
const MAX_RESPONSE_BYTES = 64 * 1024;

export interface HttpProviderOptions {
  /** Full provider id ("http:<name>"). */
  id: string;
  displayName?: string;
  url: string;
  bearerToken?: string;
  headers?: Record<string, string>;
  allowPrivateNetwork?: boolean;
  timeoutMs?: number;
  /** Optional GET endpoint for healthy(); without it the provider reports healthy. */
  healthUrl?: string;
}

export class HttpProvider implements DecisionProvider {
  readonly type = "http" as const;
  readonly id: string;
  readonly displayName: string;

  constructor(private readonly o: HttpProviderOptions) {
    this.id = o.id;
    this.displayName = o.displayName || `Policy webhook (${o.id.replace(/^http:/, "")})`;
  }

  async decide(input: DecideInput, ctx: DecisionContext): Promise<ProviderDecision> {
    const body = {
      session_id: input.sessionId,
      tool_name: input.toolName,
      input_json: input.inputJson,
      input: input.input,
      cwd: input.cwd,
      additional_directories: input.additionalDirectories,
      recent_user_messages: input.recentUserMessages,
      assistant_rationale: input.assistantRationale,
      facts: {
        subcommands: input.facts.subcommands,
        write_paths: input.facts.writePaths,
        network_hosts: input.facts.networkHosts,
        inside_git_repo: input.facts.insideGitRepo,
        git_branch: input.facts.gitBranch,
      },
      user_id: ctx.principal.user.id,
      user_role: ctx.principal.user.role,
      device_id: ctx.principal.deviceId ?? "",
    };
    const timeoutMs = this.o.timeoutMs ?? DEFAULT_HTTP_TIMEOUT_MS;
    const timeout = AbortSignal.timeout(timeoutMs);
    const signal = ctx.signal ? AbortSignal.any([ctx.signal, timeout]) : timeout;
    const headers: Record<string, string> = {
      ...this.o.headers,
      "content-type": "application/json",
      accept: "application/json",
      "user-agent": "harness-core/decision",
    };
    if (this.o.bearerToken) headers.authorization = `Bearer ${this.o.bearerToken}`;

    let text: string;
    try {
      const res = await guardedFetch(this.o.url, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal,
        allowPrivateNetwork: !!this.o.allowPrivateNetwork,
      });
      if (!res.ok) {
        await res.body?.cancel();
        throw new ProviderError(`Policy webhook returned HTTP ${res.status}`, "upstream");
      }
      text = await res.text();
    } catch (err) {
      if (err instanceof ProviderError) throw err;
      if (timeout.aborted) throw new ProviderError(`Policy webhook timed out after ${timeoutMs} ms`, "timeout");
      if (err instanceof ApiError) throw new ProviderError(`Policy webhook blocked: ${err.message}`, "config");
      throw new ProviderError(`Policy webhook is unreachable: ${(err as Error).message}`, "upstream");
    }
    if (text.length > MAX_RESPONSE_BYTES) throw new ProviderError("Policy webhook response is too large", "invalid_response");
    let raw: unknown;
    try {
      raw = JSON.parse(text);
    } catch {
      throw new ProviderError("Policy webhook returned invalid JSON", "invalid_response");
    }
    const v = parseVerdict(raw);
    if (typeof v === "string") throw new ProviderError(`Policy webhook returned an invalid decision: ${v}`, "invalid_response");
    return { ...v, reason: v.reason || `Decided by ${this.displayName}` };
  }

  async healthy(signal?: AbortSignal): Promise<boolean> {
    if (!this.o.healthUrl) return true;
    try {
      const s = signal ? AbortSignal.any([signal, AbortSignal.timeout(3_000)]) : AbortSignal.timeout(3_000);
      const res = await guardedFetch(this.o.healthUrl, { method: "GET", signal: s, allowPrivateNetwork: !!this.o.allowPrivateNetwork });
      await res.body?.cancel();
      return res.ok;
    } catch {
      return false;
    }
  }
}
