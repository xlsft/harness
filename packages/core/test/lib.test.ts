import { describe, expect, it } from "vitest";
import { decryptSecret, deriveKey, encryptSecret } from "../src/lib/crypto.ts";
import { normalizeUserCode } from "../src/lib/ids.ts";
import { parseSse } from "../src/lib/sse.ts";
import { assertUrlAllowed, isBlockedAddress } from "../src/lib/ssrf.ts";

async function parseAll(chunks: string[]) {
  const enc = new TextEncoder();
  const out = [];
  for await (const m of parseSse((async function* () { for (const c of chunks) yield enc.encode(c); })())) out.push(m);
  return out;
}

describe("SSE parser", () => {
  it("handles CRLF, split chunks, comments and multi-line data", async () => {
    const msgs = await parseAll([": comment\r\nevent: a\r", "\ndata: 1\r\ndata: 2\r\n\r\ndata: {\"x\"", ":1}\n\n", "data: tail"]);
    expect(msgs).toEqual([
      { event: "a", data: "1\n2" },
      { event: "message", data: '{"x":1}' },
      { event: "message", data: "tail" },
    ]);
  });
});

describe("secret encryption", () => {
  it("round-trips and is bound to its AAD and key", () => {
    const key = deriveKey("t", "s".repeat(40));
    const enc = encryptSecret("sk-123", "prv_1:api_key", key);
    expect(enc).not.toContain("sk-123");
    expect(decryptSecret(enc, "prv_1:api_key", key)).toBe("sk-123");
    expect(() => decryptSecret(enc, "prv_2:api_key", key)).toThrow();
    expect(() => decryptSecret(enc, "prv_1:api_key", deriveKey("t", "x".repeat(40)))).toThrow();
    expect(encryptSecret("sk-123", "a", key)).not.toBe(encryptSecret("sk-123", "a", key)); // random IV
  });
});

describe("SSRF guard", () => {
  it("blocks private, loopback, link-local, metadata and mapped addresses", () => {
    for (const ip of ["127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "::1", "fd00:ec2::254", "fe80::1", "::ffff:127.0.0.1", "::ffff:a9fe:a9fe", "2002:7f00:1::1"]) {
      expect(isBlockedAddress(ip), ip).toBe(true);
    }
    for (const ip of ["8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"]) expect(isBlockedAddress(ip), ip).toBe(false);
  });

  it("validates URLs", () => {
    expect(() => assertUrlAllowed("ftp://x", true)).toThrow(/http/);
    expect(() => assertUrlAllowed("http://user:pw@api.example.com", false)).toThrow(/Credentials/);
    expect(() => assertUrlAllowed("http://[::1]:80", false)).toThrow(/private/);
    expect(() => assertUrlAllowed("http://localhost:11434", false)).toThrow(/private/);
    expect(assertUrlAllowed("http://localhost:11434", true).port).toBe("11434");
    expect(assertUrlAllowed("https://api.openai.com/v1", false).host).toBe("api.openai.com");
  });
});

describe("user codes", () => {
  it("normalizes user input", () => {
    expect(normalizeUserCode("wdjb mjht")).toBe("WDJB-MJHT");
    expect(normalizeUserCode("WDJB-MJHT")).toBe("WDJB-MJHT");
  });
});
