/**
 * Authorization Code with PKCE for company tracker and knowledge connections, through one redirect target
 * (/api/tracker/oauth/callback). The state is the flow, sealed with the server's key, so nothing is stored until it comes back.
 */
import { createHash, randomBytes } from "node:crypto";
import { config } from "../env.ts";
import { decryptSecret, deriveKey, encryptSecret } from "../lib/crypto.ts";

const OAUTH_FLOW_TTL_MS = 10 * 60_000;
const AAD = "tracker-oauth-state";
const key = () => deriveKey("tracker-secrets");

export const oauthRedirectUri = () => `${config().publicUrl}/api/tracker/oauth/callback`;

export interface OAuthFlow {
  /** Which table the connection is in. Flows sealed before knowledge connections had none: tracker. */
  kind?: "tracker" | "knowledge";
  id: string;
  verifier: string;
  exp: number;
}

/** A new flow: the state to send and the PKCE challenge. */
export function newFlow(kind: "tracker" | "knowledge", id: string): { state: string; challenge: string } {
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  const state = encryptSecret(JSON.stringify({ kind, id, verifier, exp: Date.now() + OAUTH_FLOW_TTL_MS } satisfies OAuthFlow), AAD, key());
  return { state, challenge };
}

/** The flow a state names, or why it can't be used. */
export function openFlow(state: string): OAuthFlow | { error: string } {
  let flow: OAuthFlow;
  try {
    flow = JSON.parse(decryptSecret(state, AAD, key())) as OAuthFlow;
  } catch {
    return { error: "This authorization link is not valid." };
  }
  if (Date.now() > flow.exp) return { error: "This authorization took too long. Start it again." };
  return flow;
}
