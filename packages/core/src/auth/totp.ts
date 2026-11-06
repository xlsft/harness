/** TOTP (RFC 6238) with the parameters every authenticator app takes: SHA-1, 6 digits, 30 seconds. */
import { createHmac, randomBytes, timingSafeEqual } from "node:crypto";

const STEP_SEC = 30;
const DIGITS = 6;
const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export function base32Encode(buf: Buffer): string {
  let bits = 0;
  let value = 0;
  let out = "";
  for (const byte of buf) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += ALPHABET[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
    value &= (1 << bits) - 1;
  }
  if (bits > 0) out += ALPHABET[(value << (5 - bits)) & 31];
  return out;
}

export function base32Decode(s: string): Buffer {
  const bytes: number[] = [];
  let bits = 0;
  let value = 0;
  for (const c of s.toUpperCase().replace(/[=\s]/g, "")) {
    const i = ALPHABET.indexOf(c);
    if (i < 0) throw new Error("Invalid base32");
    value = (value << 5) | i;
    bits += 5;
    if (bits >= 8) {
      bytes.push((value >>> (bits - 8)) & 255);
      bits -= 8;
    }
    value &= (1 << bits) - 1;
  }
  return Buffer.from(bytes);
}

/** A new secret: 160 bits, base32. */
export function newTotpSecret(): string {
  return base32Encode(randomBytes(20));
}

export function stepOf(ms: number): number {
  return Math.floor(ms / 1000 / STEP_SEC);
}

/** The code of a time step (HOTP, RFC 4226). */
export function totpAt(secret: string, step: number): string {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(step));
  const mac = createHmac("sha1", base32Decode(secret)).update(counter).digest();
  const off = mac[mac.length - 1]! & 15;
  const n = mac.readUInt32BE(off) & 0x7fffffff;
  return String(n % 10 ** DIGITS).padStart(DIGITS, "0");
}

/**
 * The time step `code` belongs to, within one step of `now` for clock drift, later than `after` so that a code cannot be
 * used twice. Undefined when it does not match.
 */
export function verifyTotp(secret: string, code: string, now: number, after: number): number | undefined {
  const given = Buffer.from(code.replace(/\s/g, ""));
  if (given.length !== DIGITS) return undefined;
  const current = stepOf(now);
  let found: number | undefined;
  for (const step of [current - 1, current, current + 1]) {
    if (step <= after) continue;
    // All candidates are compared, in constant time, whichever matches.
    if (timingSafeEqual(Buffer.from(totpAt(secret, step)), given)) found = step;
  }
  return found;
}

export function otpauthUri(issuer: string, account: string, secret: string): string {
  const label = `${encodeURIComponent(issuer)}:${encodeURIComponent(account)}`;
  return `otpauth://totp/${label}?secret=${secret}&issuer=${encodeURIComponent(issuer)}&algorithm=SHA1&digits=${DIGITS}&period=${STEP_SEC}`;
}
