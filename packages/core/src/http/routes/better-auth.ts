import { defineHandler } from "h3";
import { getAuth } from "../../auth/better-auth.ts";
import { AUTH_PEER_IP_HEADER } from "../../auth/ldap-sign-in.ts";
import { clientIp } from "../../rpc/context.ts";

/**
 * Better Auth handles everything else under /api/auth (sign-in, sessions, admin plugin). The client address
 * goes along in an internal header (for the LDAP sign-in's failed-attempt limit); a client-sent one is dropped.
 */
export default defineHandler((event) => {
  let req = event.req;
  const ip = clientIp((req as { ip?: string }).ip, req.headers.get("x-forwarded-for"));
  try {
    req.headers.delete(AUTH_PEER_IP_HEADER);
    if (ip) req.headers.set(AUTH_PEER_IP_HEADER, ip);
  } catch {
    // Immutable headers: copy the request.
    const headers = new Headers(req.headers);
    headers.delete(AUTH_PEER_IP_HEADER);
    if (ip) headers.set(AUTH_PEER_IP_HEADER, ip);
    req = new Request(req, { headers }) as typeof req;
  }
  return getAuth().handler(req);
});
