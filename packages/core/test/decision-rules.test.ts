/**
 * Table-driven tests for the deterministic `rules` decision provider. The table lives in
 * test/fixtures/decision-rules.json so the Go client can run the same cases against its port.
 */
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { evaluateRules, normalizePath, parseShell, resolvePath, type RulesInput } from "../src/decision/rules.ts";

interface Case {
  name: string;
  input: RulesInput;
  expected: "allow" | "deny" | "ask";
  categories: string[];
}

const table = JSON.parse(readFileSync(new URL("./fixtures/decision-rules.json", import.meta.url), "utf8")) as {
  defaults: { cwd: string; additional_directories: string[] };
  cases: Case[];
};

function withDefaults(input: RulesInput): RulesInput {
  return { cwd: table.defaults.cwd, additional_directories: table.defaults.additional_directories, ...input };
}

describe("rules table (test/fixtures/decision-rules.json)", () => {
  it("has enough cases of every outcome", () => {
    const by = (d: string) => table.cases.filter((c) => c.expected === d).length;
    expect(table.cases.length).toBeGreaterThanOrEqual(60);
    expect(by("allow")).toBeGreaterThanOrEqual(15);
    expect(by("deny")).toBeGreaterThanOrEqual(30);
    expect(by("ask")).toBeGreaterThanOrEqual(15);
    expect(new Set(table.cases.map((c) => c.name)).size).toBe(table.cases.length);
  });

  it.each(table.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    const r = evaluateRules(withDefaults(c.input));
    expect({ decision: r.decision, categories: [...r.categories].sort() }, `${r.rule}: ${r.reason}`).toEqual({
      decision: c.expected,
      categories: [...c.categories].sort(),
    });
    expect(r.reason.length).toBeGreaterThan(0);
  });
});

describe("rules options", () => {
  const bash = (command: string, extra: Partial<RulesInput> = {}): RulesInput => withDefaults({ tool_name: "Bash", input: { command }, ...extra });

  it("admin extra deny matches the whole command and simple commands", () => {
    const opts = { extraDeny: [{ pattern: "\\bnpm publish\\b", reason: "Publishing is done by CI", categories: ["production_change" as const] }] };
    const r = evaluateRules(bash("npm test && npm publish --access public"), opts);
    expect(r).toMatchObject({ decision: "deny", reason: "Publishing is done by CI", categories: ["production_change"], rule: "admin.deny" });
    expect(evaluateRules(bash("npm test"), opts).decision).toBe("allow");
  });

  it("admin extra allow covers a simple command but never overrides a deny", () => {
    const opts = { extraAllow: ["^make deploy-docs$"] };
    expect(evaluateRules(bash("make deploy-docs"), opts).decision).toBe("allow");
    expect(evaluateRules(bash("make deploy-docs && rm -rf /"), opts).decision).toBe("deny");
    expect(evaluateRules(bash("make deploy-docs && make deploy-api"), opts).decision).toBe("ask");
  });

  it("extra rules apply to non-Bash tools as `<tool> <input_json>`", () => {
    const opts = { extraDeny: [{ pattern: "^WebFetch .*pastebin\\.com" }], extraAllow: ["^mcp__github__get_issue "] };
    expect(evaluateRules(withDefaults({ tool_name: "WebFetch", input: { url: "https://pastebin.com/raw/x" } }), opts).decision).toBe("deny");
    expect(evaluateRules(withDefaults({ tool_name: "mcp__github__get_issue", input: { number: 1 } }), opts).decision).toBe("allow");
  });

  it("invalid admin regexes are ignored", () => {
    expect(evaluateRules(bash("ls"), { extraDeny: [{ pattern: "(" }], extraAllow: ["["] }).decision).toBe("allow");
  });

  it("deny_all_force_push blocks force pushes to any branch", () => {
    const cmd = "git push --force-with-lease origin feature/x";
    expect(evaluateRules(bash(cmd)).decision).toBe("ask");
    expect(evaluateRules(bash(cmd), { denyAllForcePush: true })).toMatchObject({ decision: "deny", categories: ["git_history_rewrite"] });
  });

  it("protected branches are configurable globs", () => {
    const cmd = "git push -f origin develop";
    expect(evaluateRules(bash(cmd)).decision).toBe("ask");
    expect(evaluateRules(bash(cmd), { protectedBranches: ["dev*"] }).decision).toBe("deny");
  });

  it("client subcommands are checked as well", () => {
    const r = evaluateRules(bash("./run-everything.sh", { facts: { subcommands: ["./run-everything.sh", "rm -rf ~"] } }));
    expect(r.decision).toBe("deny");
  });

  it("accepts the tool input as JSON text", () => {
    expect(evaluateRules(withDefaults({ tool_name: "Bash", input: JSON.stringify({ command: "ls" }) })).decision).toBe("allow");
    expect(evaluateRules(withDefaults({ tool_name: "Bash", input: "not json" })).decision).toBe("ask");
  });

  it("cannot allow anything without a working directory, but still denies", () => {
    expect(evaluateRules({ tool_name: "Bash", input: { command: "pnpm test" } }).decision).toBe("ask");
    expect(evaluateRules({ tool_name: "Bash", input: { command: "ls" } }).decision).toBe("allow");
    expect(evaluateRules({ tool_name: "Bash", input: { command: "rm -rf /" } }).decision).toBe("deny");
  });
});

describe("shell parser and paths", () => {
  it("splits lists, pipelines, substitutions and heredocs", () => {
    const s = parseShell("A=1 cmd 'x y' \"$(inner a)\" | tail -n1 && other <<EOF\nbody\nEOF\nlast");
    expect(s.items.map((i) => i.sep)).toEqual(["&&", "\n", ""]);
    const first = s.items[0]!.pipeline.cmds;
    expect(first.map((c) => c.words[0]!.text)).toEqual(["cmd", "tail"]);
    expect(first[0]!.assigns[0]!.text).toBe("A=1");
    expect(first[0]!.words[1]!.text).toBe("x y");
    expect(first[0]!.words[2]!.subs[0]!.items[0]!.pipeline.cmds[0]!.words[0]!.text).toBe("inner");
    expect(s.items[1]!.pipeline.cmds[0]!.redirects[0]).toMatchObject({ op: "<<", body: "body" });
  });

  it("normalizes and resolves paths", () => {
    expect(normalizePath("/a/./b/../c//")).toBe("/a/c");
    expect(normalizePath("/..")).toBe("/");
    expect(normalizePath("~/x/..")).toBe("~");
    expect(normalizePath("~/..")).toBe("/");
    expect(resolvePath("$HOME/.ssh", "/w")).toBe("~/.ssh");
    expect(resolvePath("../x", "/w/p")).toBe("/w/x");
    expect(resolvePath("$DIR/x", "/w")).toBeUndefined();
    expect(resolvePath("rel", undefined)).toBeUndefined();
  });
});
