import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildBundle, DYNAMIC_BOUNDARY, parseOutputStyle, resolvePromptFiles } from "../src/services/prompts.ts";
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
  it("builds the shipped bundle with all M0 + M1 sections and tools", () => {
    const b = buildBundle(resolvePromptFiles(PROMPTS, ["claude-sonnet"]), "claude-sonnet");
    expect(b.system.map((s) => s.id)).toEqual(["identity", "safety", "style", "doing_tasks", "tool_policy", DYNAMIC_BOUNDARY, "env"]);
    expect(Object.keys(b.tools).sort()).toEqual([
      "AskUserQuestion", "Bash", "BrowserClick", "BrowserConsole", "BrowserEval", "BrowserKey", "BrowserNavigate", "BrowserNetwork",
      "BrowserScreenshot", "BrowserScroll", "BrowserSnapshot", "BrowserTabs", "BrowserType", "BrowserWait",
      "Edit", "EnterPlanMode", "ExitPlanMode", "Glob", "Grep", "MarkChapter", "McpAuthenticate", "Read",
      "SessionArchive", "SessionDismiss", "SessionHandoff", "SessionList", "SessionSend", "SessionStart", "Show", "TaskOutput",
      "TaskStop", "TodoWrite", "ToolSearch", "Visual", "Wait", "WebFetch", "WebSearch", "WrapUp", "Write",
    ]);
    // every reminder id listed in prompts.proto, with its placeholders
    const reminders: Record<string, string[]> = {
      project_instructions: ["content"],
      file_changed: ["path", "diff"],
      empty_file: ["path"],
      interrupted: [],
      interrupted_tool_use: [],
      interrupt_reply: [],
      todo_nudge: [],
      plan_mode: ["plan_file_path"],
      auto_mode: [],
      plan_approved: ["plan_file_path"],
      plan_rejected: ["feedback"],
      background_task_done: ["task_id", "command", "status", "tail", "output_path"],
      compacted: [],
      attribution: ["commit", "pr"],
      output_style: ["name", "content"],
      deferred_tools: ["tools"],
      environment_changed: ["directories", "variables"],
      user_shell_command: ["cwd", "command", "exit_code", "output", "status"],
      usage_limit_wrap_up: ["provider", "detail", "resets"],
      usage_limit_returned: ["provider"],
      session_message: ["from_id", "from_name", "from_title", "message"],
      session_report: ["session_id", "name", "title", "status", "report"],
      // the WebFetch summarization prompt, filled by the client for the small-slot model
      webfetch_summary: ["url", "prompt", "content"],
      // Bash tool_result notes about a command that runs in the background, by reason
      bash_background_user: ["task_id", "output_path"],
      bash_background_queued_message: ["task_id", "output_path"],
      bash_background_agent: ["task_id", "output_path"],
      bash_background_timeout: ["timeout", "task_id", "output_path"],
      // the side chat system prompt (no placeholders: the client sends the main session as a separate block)
      side_chat: [],
    };
    expect(Object.keys(b.reminders).sort()).toEqual(Object.keys(reminders).sort());
    for (const [id, phs] of Object.entries(reminders)) {
      const used = [...b.reminders[id]!.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();
      expect(used, id).toEqual([...phs].sort());
    }
    expect(b.reminders.interrupted).toBe("[Request interrupted by user]");
    expect(b.reminders.side_chat).toMatch(/no tools/);
    expect(b.reminders.side_chat).toContain("<main_session>");
    expect(b.compaction.instruction).toMatch(/summary/);
    for (const h of ["User intent", "Decisions", "Files touched", "Current state", "Next steps", "Pending todos"]) expect(b.compaction.instruction).toContain(`## ${h}`);
    expect(b.title.instruction).toMatch(/title/);
    const env = b.system.find((s) => s.id === "env") as { text: string };
    for (const ph of ["cwd", "platform", "shell", "os_version", "date", "is_git", "git_status", "model_display_name", "model_id", "additional_directories", "scratchpad_dir"]) expect(env.text).toContain(`{{${ph}}}`);
    // built-in output styles
    expect(Object.keys(b.output_styles).sort()).toEqual(["concise", "default", "explanatory", "learning", "proactive"]);
    for (const [id, s] of Object.entries(b.output_styles)) {
      expect(s.id).toBe(id);
      expect(s.name, id).not.toBe("");
      expect(s.description, id).not.toBe("");
      expect(s.keep_coding_instructions, id).toBe(true);
      expect(s.text, id).not.toMatch(/^---/);
      if (id !== "default") expect(s.text, id).not.toBe("");
    }
    expect(b.output_styles.default!.text).toBe("");
    expect(b.output_styles.learning!.text).toContain("TODO(human)");
  });

  it("parses output style frontmatter and applies overrides to styles", () => {
    expect(parseOutputStyle("plain", "Just text\n")).toEqual({ id: "plain", name: "Plain", description: "", text: "Just text", keep_coding_instructions: true });
    expect(parseOutputStyle("x", '---\nname: "Pirate"\ndescription: Talks like a pirate\nkeep-coding-instructions: false\n---\n\nArr.\n')).toEqual({
      id: "x", name: "Pirate", description: "Talks like a pirate", text: "Arr.", keep_coding_instructions: false,
    });
    const dir = tree({
      "output-styles/default.md": "---\nname: Default\ndescription: d\n---\n",
      "output-styles/terse.md": "---\nname: Terse\ndescription: t\n---\nBe terse.",
      "overrides/qwen-*/output-styles/terse.md": "---\nname: Terse\ndescription: t\n---\nBe very terse.",
      "overrides/qwen-*/output-styles/default.md": "",
    });
    const base = buildBundle(resolvePromptFiles(dir, ["gpt-x"]), "gpt-x");
    expect(Object.keys(base.output_styles)).toEqual(["default", "terse"]);
    expect(base.output_styles.terse!.text).toBe("Be terse.");
    const qwen = buildBundle(resolvePromptFiles(dir, ["qwen-flash"]), "qwen-flash");
    expect(Object.keys(qwen.output_styles)).toEqual(["terse"]);
    expect(qwen.output_styles.terse!.text).toBe("Be very terse.");
    expect(qwen.version).not.toBe(base.version);
  });

  it("ships draft-07 tool schemas with additionalProperties:false and Claude Code parameter names", () => {
    const b = buildBundle(resolvePromptFiles(PROMPTS, ["x"]), "x");
    const props = (n: string) => Object.keys((b.tools[n]!.input_schema.properties as object) ?? {});
    for (const t of Object.values(b.tools)) {
      expect(t.input_schema.$schema).toBe("http://json-schema.org/draft-07/schema#");
      expect(t.input_schema.additionalProperties).toBe(false);
      expect(t.input_schema.type).toBe("object");
    }
    expect(props("Read")).toEqual(["file_path", "offset", "limit", "description"]);
    expect(props("Write")).toEqual(["file_path", "content", "description"]);
    expect(props("Edit")).toEqual(["file_path", "old_string", "new_string", "replace_all", "description"]);
    expect(props("Bash")).toEqual(["command", "timeout", "description", "run_in_background"]);
    expect(props("TaskStop")).toEqual(["task_id", "description"]);
    expect(props("Show")).toEqual(["target", "path", "line", "end_line", "url", "description"]);
    expect(b.tools.Show!.input_schema.required).toEqual(["target", "description"]);
    expect((b.tools.Show!.input_schema.properties as any).target.enum).toEqual(["file", "diff", "url"]);
    expect(b.tools.Show!.description).toMatch(/http and https/);
    expect(props("Visual")).toEqual(["id", "title", "html"]);
    expect(props("BrowserNavigate")).toEqual(["url", "action", "description"]);
    expect(props("BrowserClick")).toEqual(["ref", "x", "y", "button", "double", "modifiers", "description"]);
    expect(props("BrowserType")).toEqual(["ref", "text", "clear", "submit", "description"]);
    expect(b.tools.BrowserType!.input_schema.required).toEqual(["text", "description"]);
    expect(props("BrowserWait")).toEqual(["text", "text_gone", "selector", "url", "navigation", "timeout", "description"]);
    for (const n of Object.keys(b.tools).filter((n) => n.startsWith("Browser"))) {
      // Page content is untrusted; every browser tool that returns it says so.
      if (!["BrowserClick", "BrowserType", "BrowserKey", "BrowserScroll", "BrowserWait"].includes(n)) expect(b.tools[n]!.description).toMatch(/untrusted/i);
    }
    expect(b.tools.BrowserType!.description).toMatch(/password/);
    expect(b.tools.Visual!.description).toMatch(/harness\.send/);
    expect(props("TaskOutput")).toEqual(["task_id", "offset", "block", "pattern", "timeout", "description"]);
    expect(b.tools.TaskOutput!.input_schema.required).toEqual(["task_id", "description"]);
    expect((b.tools.TaskOutput!.input_schema.properties as any).timeout.maximum).toBe(600000);
    expect(b.tools.TaskOutput!.description).toMatch(/30000/);
    expect(props("Wait")).toEqual(["task_id", "port", "host", "path", "changed", "pattern", "timeout", "description"]);
    expect(b.tools.Wait!.input_schema.required).toEqual(["timeout", "description"]);
    expect((b.tools.Wait!.input_schema.properties as any).timeout.maximum).toBe(600000);
    expect(b.tools.Wait!.description).toMatch(/exactly one/);
    expect(b.tools.Bash!.description).toMatch(/TaskOutput/);
    expect(b.tools.Bash!.description).toMatch(/\bWait\b/);
    expect(props("ToolSearch")).toEqual(["query", "max_results"]);
    expect(b.tools.ToolSearch!.input_schema.required).toEqual(["query"]);
    expect(props("McpAuthenticate")).toEqual([]);
    expect(b.tools.McpAuthenticate!.description).toContain("{{server}}");
    expect(props("EnterPlanMode")).toEqual([]);
    expect(props("ExitPlanMode")).toEqual(["plan"]);
    expect(b.tools.ExitPlanMode!.input_schema.required).toEqual(["plan"]);
    expect(props("TodoWrite")).toEqual(["todos"]);
    const todo = (b.tools.TodoWrite!.input_schema.properties as any).todos.items;
    expect(Object.keys(todo.properties)).toEqual(["content", "activeForm", "status"]);
    expect(todo.properties.status.enum).toEqual(["pending", "in_progress", "completed"]);
    expect(todo.additionalProperties).toBe(false);
    const qs = (b.tools.AskUserQuestion!.input_schema.properties as any).questions;
    expect([qs.minItems, qs.maxItems]).toEqual([1, 4]);
    expect(Object.keys(qs.items.properties)).toEqual(["question", "header", "type", "options", "min", "max", "allowOther"]);
    expect(qs.items.required).toEqual(["question", "header", "type"]);
    expect(qs.items.properties.type.enum).toEqual(["single", "multi", "text", "yes_no"]);
    expect(qs.items.properties.header.maxLength).toBe(12);
    expect([qs.items.properties.options.minItems, qs.items.properties.options.maxItems]).toEqual([2, 6]);
    expect(Object.keys(qs.items.properties.options.items.properties)).toEqual(["label", "description", "recommended"]);
    expect(qs.items.properties.options.items.required).toEqual(["label"]);
    expect(qs.items.additionalProperties).toBe(false);
    expect(qs.items.properties.options.items.additionalProperties).toBe(false);
    expect(b.tools.Bash!.description).toMatch(/run_in_background/);
    expect(b.tools.Bash!.description).toMatch(/TaskStop/);
    expect(props("Glob")).toEqual(["pattern", "path", "description"]);
    expect(props("Grep")).toEqual(["pattern", "path", "glob", "type", "output_mode", "-i", "-n", "-A", "-B", "-C", "multiline", "head_limit", "description"]);
    expect(b.tools.Bash!.description).toMatch(/30000 characters/);
    expect(b.tools.Bash!.description).toMatch(/600000/);
    expect(props("WebSearch")).toEqual(["query", "allowed_domains", "blocked_domains", "description"]);
    expect(b.tools.WebSearch!.input_schema.required).toEqual(["query", "description"]);
    expect(props("WebFetch")).toEqual(["url", "prompt", "description"]);
    expect(b.tools.WebFetch!.input_schema.required).toEqual(["url", "prompt", "description"]);
    expect(b.tools.WebFetch!.description).toMatch(/15 minutes/);
    expect(b.tools.WebFetch!.description).toMatch(/10 MB/);
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
    expect(JSON.parse(b.tools.Edit!.inputSchemaJson).required).toEqual(["file_path", "old_string", "new_string", "description"]);
    expect(b.compactionInstruction).not.toBe("");
    expect(Object.keys(b.outputStyles).sort()).toEqual(["concise", "default", "explanatory", "learning", "proactive"]);
    expect(b.outputStyles.explanatory).toMatchObject({ id: "explanatory", name: "Explanatory", keepCodingInstructions: true });
    expect(b.reminders.output_style).toContain("{{content}}");

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
