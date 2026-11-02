import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildBundle, DYNAMIC_BOUNDARY, resolvePromptFiles } from "../src/services/prompts.ts";
import { adminApi, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

const PROMPTS = resolve(import.meta.dirname, "../prompts");

function tree(files: Record<string, string>): string {
  const dir = mkdtempSync(join(tmpdir(), "harness-prompts-"));
  for (const [p, c] of Object.entries(files)) {
    mkdirSync(dirname(join(dir, p)), { recursive: true });
    writeFileSync(join(dir, p), c);
  }
  return dir;
}

describe("bundle builder", () => {
  it("builds the shipped bundle with all M0 sections and tools", () => {
    const b = buildBundle(resolvePromptFiles(PROMPTS, ["claude-sonnet"]), "claude-sonnet");
    expect(b.system.map((s) => s.id)).toEqual(["identity", "safety", "style", "doing_tasks", "tool_policy", DYNAMIC_BOUNDARY, "env"]);
    expect(Object.keys(b.tools).sort()).toEqual(["Bash", "Edit", "Glob", "Grep", "Read", "Write"]);
    expect(Object.keys(b.reminders).sort()).toEqual(["empty_file", "file_changed", "interrupted", "project_instructions"]);
    expect(b.reminders.interrupted).toBe("[Request interrupted by user]");
    expect(b.compaction.instruction).toMatch(/summary/);
    expect(b.title.instruction).toMatch(/title/);
    const env = b.system.find((s) => s.id === "env") as { text: string };
    for (const ph of ["cwd", "platform", "shell", "os_version", "date", "is_git", "model_display_name", "model_id"]) expect(env.text).toContain(`{{${ph}}}`);
  });

  it("ships draft-07 tool schemas with additionalProperties:false and Claude Code parameter names", () => {
    const b = buildBundle(resolvePromptFiles(PROMPTS, ["x"]), "x");
    const props = (n: string) => Object.keys((b.tools[n]!.input_schema.properties as object) ?? {});
    for (const t of Object.values(b.tools)) {
      expect(t.input_schema.$schema).toBe("http://json-schema.org/draft-07/schema#");
      expect(t.input_schema.additionalProperties).toBe(false);
      expect(t.input_schema.type).toBe("object");
    }
    expect(props("Read")).toEqual(["file_path", "offset", "limit"]);
    expect(props("Write")).toEqual(["file_path", "content"]);
    expect(props("Edit")).toEqual(["file_path", "old_string", "new_string", "replace_all"]);
    expect(props("Bash")).toEqual(["command", "timeout", "description"]);
    expect(props("Glob")).toEqual(["pattern", "path"]);
    expect(props("Grep")).toEqual(["pattern", "path", "glob", "type", "output_mode", "-i", "-n", "-A", "-B", "-C", "multiline", "head_limit"]);
    expect(b.tools.Bash!.description).toMatch(/30000 characters/);
    expect(b.tools.Bash!.description).toMatch(/600000/);
  });

  it("applies overrides by specificity; empty files delete; version tracks content", () => {
    const dir = tree({
      "system/static/10-identity.md": "base identity\n",
      "system/static/20-style.md": "base style",
      "system/dynamic/10-env.md": "cwd={{cwd}}",
      "tools/Read/description.md": "read",
      "tools/Read/schema.json": '{"type":"object"}',
      "tools/Grep/description.md": "grep",
      "tools/Grep/schema.json": '{"type":"object"}',
      "overrides/gpt-*/system/static/20-style.md": "gpt style",
      "overrides/gpt-5-mini/system/static/20-style.md": "mini style",
      "overrides/gpt-*/tools/Grep/description.md": "",
      "overrides/*mini*/system/static/30-extra.md": "extra",
    });
    const base = buildBundle(resolvePromptFiles(dir, ["claude"]), "claude");
    expect(base.system).toEqual([{ id: "identity", text: "base identity" }, { id: "style", text: "base style" }, { id: DYNAMIC_BOUNDARY }, { id: "env", text: "cwd={{cwd}}" }]);
    const gpt = buildBundle(resolvePromptFiles(dir, ["gpt-big"]), "gpt-big");
    expect(gpt.system[1]).toEqual({ id: "style", text: "gpt style" });
    expect(Object.keys(gpt.tools)).toEqual(["Read"]);
    // exact match wins over globs; upstream model id is matched too
    const mini = buildBundle(resolvePromptFiles(dir, ["my-alias", "gpt-5-mini"]), "my-alias");
    expect(mini.system.map((s) => ("text" in s ? s.text : s.id))).toEqual(["base identity", "mini style", "extra", DYNAMIC_BOUNDARY, "cwd={{cwd}}"]);
    expect(new Set([base.version, gpt.version, mini.version]).size).toBe(3);
    expect(buildBundle(resolvePromptFiles(dir, ["claude"]), "claude").version).toBe(base.version);
  });
});

describe("PromptService", () => {
  let ts: TestServer;
  let token: string;
  beforeAll(async () => {
    ts = await startTestServer();
    token = (await deviceLogin(ts)).accessToken;
    await adminApi(ts.url, token, "POST", "/api/admin/providers", { id: "p", type: "openai-chat", base_url: "http://127.0.0.1:1/v1", allow_private_network: true });
    await adminApi(ts.url, token, "POST", "/api/admin/models", { id: "gpt-x", provider_id: "p", upstream_model: "gpt-5" });
    await adminApi(ts.url, token, "POST", "/api/admin/models", { id: "claude-x", provider_id: "p", upstream_model: "claude-5" });
    await adminApi(ts.url, token, "PUT", "/api/admin/slots", { main: "claude-x" });
  });
  afterAll(async () => ts.close());

  it("returns a model-specific bundle and honors if_none_match", async () => {
    const prompts = ts.as(token).prompts;
    const r = await prompts.getBundle({ model: "gpt-x" });
    expect(r.notModified).toBe(false);
    const b = r.bundle!;
    expect(b.model).toBe("gpt-x");
    expect(b.system.find((s) => s.dynamicBoundary)).toMatchObject({ id: DYNAMIC_BOUNDARY, text: "" });
    expect(b.system.find((s) => s.id === "tool_policy")!.text).toMatch(/function-calling interface/); // gpt-* override
    expect(JSON.parse(b.tools.Edit!.inputSchemaJson).required).toEqual(["file_path", "old_string", "new_string"]);
    expect(b.compactionInstruction).not.toBe("");

    const again = await prompts.getBundle({ model: "gpt-x", ifNoneMatch: b.version });
    expect(again.notModified).toBe(true);
    expect(again.bundle).toBeUndefined();

    const claude = await prompts.getBundle({ model: "claude-x", ifNoneMatch: b.version });
    expect(claude.notModified).toBe(false);
    expect(claude.bundle!.version).not.toBe(b.version);

    const def = await prompts.getBundle({});
    expect(def.bundle!.model).toBe("claude-x"); // main slot
  });

  it("returns NOT_FOUND for unknown models", async () => {
    await expect(ts.as(token).prompts.getBundle({ model: "missing" })).rejects.toMatchObject({ code: 5 });
  });
});
