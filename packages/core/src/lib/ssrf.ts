/**
 * SSRF guard for provider base URLs.
 *
 * - Hostnames are resolved by our own `lookup` hook inside the undici connector, so the
 *   address that is checked is the address that is connected to (no DNS-rebinding gap).
 * - IP-literal hosts are checked before the request (undici does not call lookup for them).
 * - Redirects are followed manually and every hop is re-validated.
 * - `allowPrivateNetwork` (per-provider admin flag) disables the address checks, e.g. for a
 *   local vLLM/Ollama. Scheme checks still apply.
 */
import { lookup as dnsLookup, type LookupAddress } from "node:dns";
import { BlockList, isIP } from "node:net";
import { Agent, fetch as undiciFetch, type RequestInit as UndiciRequestInit, type Response as UndiciResponse } from "undici";
import { ApiError } from "./errors.ts";

const blocked = new BlockList();
for (const [net, prefix] of [
  ["0.0.0.0", 8],
  ["10.0.0.0", 8],
  ["100.64.0.0", 10], // CGNAT, also Alibaba metadata 100.100.100.200
  ["127.0.0.0", 8],
  ["169.254.0.0", 16], // link-local, cloud metadata 169.254.169.254
  ["172.16.0.0", 12],
  ["192.0.0.0", 24],
  ["192.0.2.0", 24],
  ["192.168.0.0", 16],
  ["198.18.0.0", 15],
  ["198.51.100.0", 24],
  ["203.0.113.0", 24],
  ["224.0.0.0", 4],
  ["240.0.0.0", 4],
] as const) {
  blocked.addSubnet(net, prefix, "ipv4");
}
for (const [net, prefix] of [
  ["::", 128],
  ["::1", 128],
  ["64:ff9b:1::", 48],
  ["100::", 64],
  ["2001:db8::", 32],
  ["fc00::", 7], // ULA, incl. AWS IMDSv6 fd00:ec2::254
  ["fe80::", 10],
  ["fec0::", 10],
  ["ff00::", 8],
] as const) {
  blocked.addSubnet(net, prefix, "ipv6");
}

/** Extracts an embedded IPv4 from IPv4-mapped/compatible, NAT64 and 6to4 IPv6 addresses. */
function embeddedIpv4(ip: string): string | undefined {
  const lower = ip.toLowerCase();
  const dotted = lower.match(/^(?:::ffff:|::|64:ff9b::)(\d+\.\d+\.\d+\.\d+)$/);
  if (dotted) return dotted[1];
  const hex = lower.match(/^(?:::ffff:|64:ff9b::)([0-9a-f]{1,4}):([0-9a-f]{1,4})$/);
  if (hex) return hexPairToV4(hex[1]!, hex[2]!);
  const sixToFour = lower.match(/^2002:([0-9a-f]{1,4}):([0-9a-f]{1,4}):/);
  if (sixToFour) return hexPairToV4(sixToFour[1]!, sixToFour[2]!);
  return undefined;
}

function hexPairToV4(a: string, b: string): string {
  const x = Number.parseInt(a, 16);
  const y = Number.parseInt(b, 16);
  return `${x >> 8}.${x & 255}.${y >> 8}.${y & 255}`;
}

export function isBlockedAddress(ip: string): boolean {
  const family = isIP(ip);
  if (family === 4) return blocked.check(ip, "ipv4");
  if (family === 6) {
    const v4 = embeddedIpv4(ip);
    if (v4 && blocked.check(v4, "ipv4")) return true;
    return blocked.check(ip, "ipv6");
  }
  return true; // not an IP at all
}

function hostOf(url: URL): string {
  return url.hostname.replace(/^\[|\]$/g, "");
}

/** Synchronous checks that do not need DNS: scheme, credentials, IP-literal host. */
export function assertUrlAllowed(raw: string | URL, allowPrivateNetwork: boolean): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new ApiError("invalid_request", `Invalid URL: ${String(raw)}`);
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new ApiError("invalid_request", "Only http(s) URLs are allowed");
  }
  if (url.username || url.password) throw new ApiError("invalid_request", "Credentials in URL are not allowed");
  const host = hostOf(url);
  if (!allowPrivateNetwork) {
    if (isIP(host) && isBlockedAddress(host)) {
      throw new ApiError("permission", `Address ${host} is in a private or reserved range (enable allow_private_network)`);
    }
    if (host === "localhost" || host.endsWith(".localhost") || host.endsWith(".internal")) {
      throw new ApiError("permission", `Host ${host} is private (enable allow_private_network)`);
    }
  }
  return url;
}

/** Resolves the host and checks every address. Used when an admin saves a provider. */
export async function assertResolvesPublic(raw: string, allowPrivateNetwork: boolean): Promise<void> {
  const url = assertUrlAllowed(raw, allowPrivateNetwork);
  if (allowPrivateNetwork) return;
  const host = hostOf(url);
  if (isIP(host)) return;
  const addrs = await new Promise<LookupAddress[]>((resolve, reject) =>
    dnsLookup(host, { all: true }, (err, a) => (err ? reject(err) : resolve(a))),
  ).catch((err: Error) => {
    throw new ApiError("invalid_request", `Cannot resolve ${host}: ${err.message}`);
  });
  const bad = addrs.find((a) => isBlockedAddress(a.address));
  if (bad) throw new ApiError("permission", `${host} resolves to private address ${bad.address} (enable allow_private_network)`);
}

class BlockedAddressError extends Error {
  code = "EHARNESS_SSRF";
}

const guardedLookup: typeof dnsLookup = ((hostname: string, options: any, callback: any) => {
  if (typeof options === "function") {
    callback = options;
    options = {};
  }
  dnsLookup(hostname, { ...options, all: true }, (err, addresses: LookupAddress[]) => {
    if (err) return callback(err);
    const bad = addresses.find((a) => isBlockedAddress(a.address));
    if (bad) return callback(new BlockedAddressError(`${hostname} resolves to blocked address ${bad.address}`));
    if (options.all) return callback(null, addresses);
    const first = addresses[0]!;
    callback(null, first.address, first.family);
  });
}) as typeof dnsLookup;

const publicAgent = new Agent({ connect: { lookup: guardedLookup }, headersTimeout: 600_000, bodyTimeout: 0 });
const privateAgent = new Agent({ headersTimeout: 600_000, bodyTimeout: 0 });

const MAX_REDIRECTS = 3;

/** fetch() for upstream providers with the SSRF policy applied to every hop. */
export async function guardedFetch(
  url: string,
  init: UndiciRequestInit & { allowPrivateNetwork: boolean },
): Promise<UndiciResponse> {
  const { allowPrivateNetwork, ...rest } = init;
  let current = assertUrlAllowed(url, allowPrivateNetwork);
  let reqInit: UndiciRequestInit = rest;
  for (let hop = 0; ; hop++) {
    let res: UndiciResponse;
    try {
      res = await undiciFetch(current, {
        ...reqInit,
        redirect: "manual",
        dispatcher: allowPrivateNetwork ? privateAgent : publicAgent,
      });
    } catch (err) {
      const cause = (err as { cause?: unknown }).cause;
      if (cause instanceof BlockedAddressError) {
        throw new ApiError("permission", `${cause.message} (enable allow_private_network)`);
      }
      throw err;
    }
    if (res.status < 300 || res.status >= 400 || res.status === 304) return res;
    const location = res.headers.get("location");
    if (!location) return res;
    if (hop >= MAX_REDIRECTS) throw new ApiError("upstream", "Too many upstream redirects");
    await res.body?.cancel();
    const next = assertUrlAllowed(new URL(location, current), allowPrivateNetwork);
    // Never forward provider credentials to a different origin.
    if (next.origin !== current.origin) throw new ApiError("upstream", `Upstream redirected to another origin (${next.origin})`);
    current = next;
    if (res.status === 303) reqInit = { ...reqInit, method: "GET", body: undefined };
  }
}

/** The undici dispatcher carrying the SSRF policy (address check at connect time), e.g. for WebSocket. */
export function guardedDispatcher(allowPrivateNetwork: boolean): Agent {
  return allowPrivateNetwork ? privateAgent : publicAgent;
}

/** Finds an SSRF rejection in an error chain and returns it as the same ApiError guardedFetch throws. */
export function ssrfError(err: unknown): ApiError | undefined {
  for (let e: unknown = err, i = 0; e && i < 6; e = (e as { cause?: unknown }).cause, i++) {
    if (e instanceof BlockedAddressError) return new ApiError("permission", `${e.message} (enable allow_private_network)`);
    if (e instanceof ApiError && e.type === "permission") return e;
  }
  return undefined;
}

/** guardedFetch with the WHATWG fetch signature, for libraries that accept a custom fetch (MCP SDK). */
export function guardedFetchFn(allowPrivateNetwork: boolean): (url: string | URL, init?: RequestInit) => Promise<Response> {
  return async (url, init) =>
    (await guardedFetch(String(url), { ...(init as UndiciRequestInit), allowPrivateNetwork })) as unknown as Response;
}
