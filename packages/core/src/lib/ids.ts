import { createHash, randomBytes, randomInt } from "node:crypto";

/** Opaque prefixed id, e.g. `usr_3kq9…`. */
export function newId(prefix: string, bytes = 12): string {
  return `${prefix}_${randomBytes(bytes).toString("base64url").replace(/[-_]/g, "x")}`;
}

/** High-entropy bearer secret with a prefix (`hdt_…`, `hat_…`). */
export function newToken(prefix: string): string {
  return `${prefix}_${randomBytes(32).toString("base64url")}`;
}

export function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

// RFC 8628 §6.1: consonants only, no ambiguous characters.
const USER_CODE_ALPHABET = "BCDFGHJKLMNPQRSTVWXZ";

/** `WDJB-MJHT` style user code (8 chars, ~34.5 bits). */
export function newUserCode(): string {
  let s = "";
  for (let i = 0; i < 8; i++) s += USER_CODE_ALPHABET[randomInt(USER_CODE_ALPHABET.length)];
  return `${s.slice(0, 4)}-${s.slice(4)}`;
}

/** Normalizes user input (`wdjb mjht`, `WDJBMJHT`) to `WDJB-MJHT`. */
export function normalizeUserCode(input: string): string {
  const s = input.toUpperCase().replace(/[^A-Z]/g, "");
  return s.length === 8 ? `${s.slice(0, 4)}-${s.slice(4)}` : s;
}
