/**
 * Prompt bundle builder (docs/protocol/prompts.md). M0: bundle is assembled from files under
 * `prompts/` (HARNESS_PROMPTS_DIR). See prompts/README.md for the file convention and
 * per-model overrides.
 */
import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { config } from "../env.ts";
import { ApiError } from "../lib/errors.ts";
import type { PublicUser } from "./devices.ts";
import { isLocalModelId } from "./local-models.ts";
import { getSlots, resolveModelForUser } from "./models.ts";

export const DYNAMIC_BOUNDARY = "__dynamic_boundary__";

export interface PromptBundle {
  version: string;
  model: string;
  system: ({ id: string; text: string } | { id: typeof DYNAMIC_BOUNDARY })[];
  tools: Record<string, { description: string; input_schema: Record<string, unknown> }>;
  reminders: Record<string, string>;
  compaction: { instruction: string };
  title: { instruction: string };
  output_styles: Record<string, OutputStyle>;
}

export interface OutputStyle {
  id: string;
  name: string;
  description: string;
  text: string;
  keep_coding_instructions: boolean;
}

/** Reads every file under `dir` into a map keyed by POSIX relative path. */
function readTree(dir: string, skipTop?: string): Map<string, string> {
  const out = new Map<string, string>();
  if (!existsSync(dir)) return out;
  const walk = (d: string) => {
    for (const name of readdirSync(d).sort()) {
      if (name.startsWith(".")) continue;
      const full = join(d, name);
      const rel = relative(dir, full).split(sep).join("/");
      if (skipTop && rel === skipTop) continue;
      if (statSync(full).isDirectory()) walk(full);
      else out.set(rel, readFileSync(full, "utf8"));
    }
  };
  walk(dir);
  return out;
}

// In directory names `@` stands for `*`: Windows forbids `*` in file names, so `overrides/gpt-@/` is the portable form.
function globToRegExp(glob: string): RegExp {
  const esc = glob.replace(/@/g, "*").replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".");
  return new RegExp(`^${esc}$`, "i");
}

/** Override directory names are globs; fewer wildcards = more specific = applied later. */
function specificity(glob: string): number {
  return glob.replace(/[*?@]/g, "").length - (glob.match(/[*?@]/g)?.length ?? 0) * 100;
}

/**
 * Resolves the file tree for a model: base files, then every matching `overrides/<glob>/`
 * directory in ascending specificity. An empty override file deletes the base file.
 */
export function resolvePromptFiles(dir: string, modelIds: string[]): Map<string, string> {
  const files = readTree(dir, "overrides");
  for (const k of [...files.keys()]) if (k === "README.md") files.delete(k);
  const overridesDir = join(dir, "overrides");
  if (existsSync(overridesDir)) {
    const globs = readdirSync(overridesDir)
      .filter((n) => !n.startsWith(".") && statSync(join(overridesDir, n)).isDirectory())
      .filter((g) => modelIds.some((id) => globToRegExp(g).test(id)))
      .sort((a, b) => specificity(a) - specificity(b) || a.localeCompare(b));
    for (const g of globs) {
      for (const [rel, content] of readTree(join(overridesDir, g))) {
        if (content.trim() === "") files.delete(rel);
        else files.set(rel, content);
      }
    }
  }
  return files;
}

const SECTION_RE = /^(?:\d+-)?(.+)\.md$/;

function sectionsFrom(files: Map<string, string>, prefix: string) {
  return [...files.keys()]
    .filter((k) => k.startsWith(prefix) && !k.slice(prefix.length).includes("/") && k.endsWith(".md"))
    .sort()
    .map((k) => ({ id: SECTION_RE.exec(k.slice(prefix.length))![1]!, text: files.get(k)!.trimEnd() }));
}

const FRONTMATTER_RE = /^---\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/;

/**
 * Parses an output style file: `---` frontmatter with `name`, `description` and
 * `keep-coding-instructions` (default true), then the style text.
 */
export function parseOutputStyle(id: string, content: string): OutputStyle {
  const m = FRONTMATTER_RE.exec(content);
  const meta: Record<string, string> = {};
  if (m) {
    for (const line of m[1]!.split(/\r?\n/)) {
      const kv = /^([\w-]+)\s*:\s*(.*)$/.exec(line.trim());
      if (kv) meta[kv[1]!.toLowerCase()] = kv[2]!.trim().replace(/^(["'])(.*)\1$/, "$2");
    }
  }
  const keep = (meta["keep-coding-instructions"] ?? "true").toLowerCase();
  return {
    id,
    name: meta.name || id.charAt(0).toUpperCase() + id.slice(1),
    description: meta.description ?? "",
    text: (m ? content.slice(m[0].length) : content).trim(),
    keep_coding_instructions: keep !== "false" && keep !== "no",
  };
}

export function buildBundle(files: Map<string, string>, modelId: string): PromptBundle {
  const tools: PromptBundle["tools"] = {};
  const toolNames = new Set(
    [...files.keys()].filter((k) => k.startsWith("tools/")).map((k) => k.split("/")[1]!),
  );
  for (const name of [...toolNames].sort()) {
    const description = files.get(`tools/${name}/description.md`);
    const schema = files.get(`tools/${name}/schema.json`);
    if (description === undefined || schema === undefined) continue; // deleted by an override
    let input_schema: Record<string, unknown>;
    try {
      input_schema = JSON.parse(schema);
    } catch (e) {
      throw new ApiError("internal", `Invalid JSON schema for tool ${name}: ${(e as Error).message}`);
    }
    tools[name] = { description: description.trimEnd(), input_schema };
  }

  const reminders: Record<string, string> = {};
  for (const s of sectionsFrom(files, "reminders/")) reminders[s.id] = s.text;

  const output_styles: Record<string, OutputStyle> = {};
  for (const k of [...files.keys()].sort()) {
    const m = /^output-styles\/([^/]+)\.md$/.exec(k);
    if (m) output_styles[m[1]!] = parseOutputStyle(m[1]!, files.get(k)!);
  }

  const body = {
    model: modelId,
    system: [
      ...sectionsFrom(files, "system/static/"),
      { id: DYNAMIC_BOUNDARY } as const,
      ...sectionsFrom(files, "system/dynamic/"),
    ],
    tools,
    reminders,
    compaction: { instruction: (files.get("compaction/instruction.md") ?? "").trimEnd() },
    title: { instruction: (files.get("title/instruction.md") ?? "").trimEnd() },
    output_styles,
  };
  const version = createHash("sha256").update(JSON.stringify(body)).digest("hex").slice(0, 16);
  return { version, ...body };
}

/** Service entry point: bundle for the given model alias (or the user's `main` slot). */
export async function getPromptBundle(user: PublicUser, modelId: string | undefined): Promise<PromptBundle> {
  let id = modelId;
  if (!id) {
    id = (await getSlots()).main;
    if (!id) throw new ApiError("invalid_request", "model query parameter is required");
  }
  if (isLocalModelId(id)) {
    // A model on the user's machine (agent.v1 localmodels.proto): in no catalog. Overrides match the full id
    // (e.g. "local:*") and the provider's model name.
    const files = resolvePromptFiles(config().promptsDir, [id, id.slice(id.indexOf("/") + 1)]);
    return buildBundle(files, id);
  }
  const model = await resolveModelForUser(id, user);
  const files = resolvePromptFiles(config().promptsDir, [model.id, model.upstreamModel]);
  return buildBundle(files, model.id);
}

/** True when an If-None-Match header matches the ETag for `version`. */
export function etagMatches(ifNoneMatch: string | null | undefined, version: string): boolean {
  if (!ifNoneMatch) return false;
  return ifNoneMatch
    .split(",")
    .map((s) => s.trim().replace(/^W\//, ""))
    .some((t) => t === "*" || t === `"${version}"`);
}
