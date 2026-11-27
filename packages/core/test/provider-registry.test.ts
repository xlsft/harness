/** The provider registry and the checks every provider gets for free. Provider authors: this must pass with your line added. */
import { describe, expect, it } from "vitest";
import { ApiError } from "../src/lib/errors.ts";
import { AUTH_METHODS } from "../src/tracker/provider.ts";
import { KNOWLEDGE, TRACKERS } from "../src/tracker/registry.ts";
import { checkAuth, checkOptions } from "../src/tracker/signin.ts";

describe("the registry", () => {
  it("has unique ids, known methods, and borrows only from trackers it lists", () => {
    const trackers = TRACKERS.map((m) => m.capabilities.provider);
    const knowledge = KNOWLEDGE.map((m) => m.capabilities.provider);
    expect(new Set([...trackers, ...knowledge]).size).toBe(trackers.length + knowledge.length);
    for (const m of [...TRACKERS, ...KNOWLEDGE]) {
      const c = m.capabilities;
      expect(c.provider).toMatch(/^[a-z][a-z0-9-]*$/);
      expect(c.displayName).not.toBe("");
      expect(c.docsUrl ?? "").toMatch(/^https:\/\//);
      for (const a of c.authMethods) expect(AUTH_METHODS).toContain(a);
      if (c.authMethods.includes("oauth")) expect(m.oauth).toBeDefined();
      for (const f of c.optionFields ?? []) expect(f.key).toMatch(/^[a-z][a-z0-9_]{0,63}$/);
    }
    for (const m of KNOWLEDGE) {
      for (const b of m.capabilities.borrowFrom) expect(trackers).toContain(b);
      expect(m.capabilities.authMethods.length + m.capabilities.borrowFrom.length).toBeGreaterThan(0);
    }
  });
});

describe("sign-in checks", () => {
  it("fill defaults, drop unknown keys and refuse what is not allowed", () => {
    const fields = [
      { key: "org_id", label: "Organization", required: true },
      { key: "kind", label: "Kind", allowedValues: ["cloud", "360"], defaultValue: "cloud" },
    ];
    expect(checkOptions(fields, { org_id: " 42 ", junk: "x" })).toEqual({ org_id: "42", kind: "cloud" });
    expect(() => checkOptions(fields, {})).toThrow(ApiError);
    expect(() => checkOptions(fields, { org_id: "1", kind: "moon" })).toThrow(/one of/);
  });

  it("need the login a method takes", () => {
    expect(() => checkAuth(["token"], "password", "a", "")).toThrow(/signs in/);
    expect(() => checkAuth(["email_token"], "email_token", "no-at", "")).toThrow(/email/);
    expect(checkAuth(["email_token"], "email_token", "me@example.test", "")).toBe("email_token");
    expect(() => checkAuth(["oauth"], "oauth", "", "")).toThrow(/client id/);
  });
});
