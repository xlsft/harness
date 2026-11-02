import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from "node:crypto";
import { config } from "../env.ts";

/** Derives a purpose-specific 32-byte key from HARNESS_SECRET (HKDF-SHA256). */
export function deriveKey(purpose: string, secret = config().secret): Buffer {
  return Buffer.from(hkdfSync("sha256", secret, "harness", purpose, 32));
}

const VERSION = "v1";

/**
 * AES-256-GCM. Output: `v1.<iv>.<tag>.<ciphertext>` (base64url parts).
 * `aad` binds the ciphertext to its owner (e.g. the provider id) so values cannot be swapped between rows.
 */
export function encryptSecret(plaintext: string, aad: string, key = deriveKey("provider-secrets")): string {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  cipher.setAAD(Buffer.from(aad));
  const ct = Buffer.concat([cipher.update(plaintext, "utf8"), cipher.final()]);
  const tag = cipher.getAuthTag();
  return [VERSION, iv.toString("base64url"), tag.toString("base64url"), ct.toString("base64url")].join(".");
}

export function decryptSecret(payload: string, aad: string, key = deriveKey("provider-secrets")): string {
  const [ver, iv, tag, ct] = payload.split(".");
  if (ver !== VERSION || !iv || !tag || ct === undefined) throw new Error("Unsupported secret format");
  const decipher = createDecipheriv("aes-256-gcm", key, Buffer.from(iv, "base64url"));
  decipher.setAAD(Buffer.from(aad));
  decipher.setAuthTag(Buffer.from(tag, "base64url"));
  return Buffer.concat([decipher.update(Buffer.from(ct, "base64url")), decipher.final()]).toString("utf8");
}
