/**
 * Deterministic `rules` decision provider: hard-deny patterns and a fast-allow list for tool calls of the
 * `auto` permission mode (proto/harness/decision/v1/decision.proto).
 *
 * Pure and dependency-free on purpose: the Go client ports this module (and its test table,
 * test/fixtures/decision-rules.json) so `auto` degrades safely when the server is unreachable.
 *
 * Outcome:
 *   - deny:  the call matches a hard-deny pattern (categories say why); final, no other provider can allow it;
 *   - allow: every simple command is read-only, a test/lint/build run inside the workspace, or an
 *            admin-allowed command, and nothing writes outside the workspace;
 *   - ask:   anything else; the chain passes the call to the next provider.
 *
 * Bash commands are parsed with a small shell parser (quotes, escapes, `$(...)`, backticks, process
 * substitution, heredocs, pipelines, `&&` / `||` / `;` lists, subshells). Wrappers (env assignments,
 * `env`, `sudo`, `nohup`, `timeout`, `xargs`, ...) are stripped and `bash -c` / `eval` bodies are parsed
 * recursively, so every simple command that would run is checked. `cd` updates the working directory
 * used to resolve later relative paths.
 */

export type RuleDecision = "allow" | "deny" | "ask";

/** Lowercase names of harness.decision.v1.RiskCategory values (without the RISK_CATEGORY_ prefix). */
export type RuleCategory =
  | "destructive"
  | "remote_code_execution"
  | "secret_exfiltration"
  | "production_change"
  | "git_history_rewrite"
  | "privilege_escalation"
  | "network_egress"
  | "out_of_scope"
  | "package_install";

export const RULE_CATEGORIES: readonly RuleCategory[] = [
  "destructive",
  "remote_code_execution",
  "secret_exfiltration",
  "production_change",
  "git_history_rewrite",
  "privilege_escalation",
  "network_egress",
  "out_of_scope",
  "package_install",
];

export interface RulesFacts {
  subcommands?: string[];
  write_paths?: string[];
  network_hosts?: string[];
  inside_git_repo?: boolean;
  git_branch?: string;
}

export interface RulesInput {
  tool_name: string;
  /** Tool input: an object or its JSON text. */
  input: Record<string, unknown> | string;
  cwd?: string;
  additional_directories?: string[];
  facts?: RulesFacts;
}

export interface ExtraDenyRule {
  /** JavaScript regular expression (no flags; case-sensitive). */
  pattern: string;
  reason?: string;
  categories?: RuleCategory[];
}

export interface RulesOptions {
  /**
   * Extra hard-deny regexes. Tested against the whole Bash command and against every simple command;
   * for other tools against `<tool_name> <input_json>`.
   */
  extraDeny?: ExtraDenyRule[];
  /**
   * Extra fast-allow regexes. A simple command matching one counts as safe (all simple commands must be
   * safe for ALLOW); for other tools tested against `<tool_name> <input_json>`. Never overrides a deny.
   */
  extraAllow?: string[];
  /** Branch globs force pushes may not target (`*` wildcard). Default: main, master, release*. */
  protectedBranches?: string[];
  /** Deny force pushes to any branch, not only protected ones. */
  denyAllForcePush?: boolean;
}

export interface RulesResult {
  decision: RuleDecision;
  reason: string;
  categories: RuleCategory[];
  /** Id of the rule that decided (for audit and tests). */
  rule: string;
}

export const DEFAULT_PROTECTED_BRANCHES = ["main", "master", "release*"];

/**
 * Version of the built-in rule set. Bump it whenever a rule changes (the Go port in
 * packages/client/decision/rulesengine.go carries the same number, and test/fixtures/decision-rules.json
 * records it); it is part of the rules_version clients use to notice changed rules.
 */
export const BUILTIN_RULES_VERSION = 2;

const MAX_COMMAND_LENGTH = 100_000;
const MAX_DEPTH = 6;

// ---------------------------------------------------------------------------------------------------
// Shell parser
// ---------------------------------------------------------------------------------------------------

export interface Word {
  /** Value after quote removal; substitutions appear as `$(…)`, parameters stay as written (`$HOME`). */
  text: string;
  quoted: boolean;
  /** Parameter expansion or command substitution somewhere in the word. */
  dynamic: boolean;
  /** Command substitutions (`$(...)`, backticks) inside the word. */
  subs: Script[];
  /** Process substitutions (`<(...)`, `>(...)`); the whole word is the substitution. */
  procSubs: Script[];
}

export interface Redirect {
  op: string;
  fd?: string;
  target: Word;
  /** Heredoc body (`<<`, `<<-`). */
  body?: string;
}

export interface SimpleCommand {
  assigns: Word[];
  words: Word[];
  redirects: Redirect[];
  /** `( ... )` subshell in place of a command. */
  group?: Script;
}

export interface Pipeline {
  cmds: SimpleCommand[];
}

export interface Script {
  items: { pipeline: Pipeline; sep: string }[];
}

export class ShellParseError extends Error {}

const ASSIGN_RE = /^[A-Za-z_][A-Za-z0-9_]*\+?=/;
const WORD_BREAK = " \t\n;&|<>()";

class Parser {
  private i = 0;
  private pending: Redirect[] = [];

  constructor(
    private readonly s: string,
    private readonly depth = 0,
  ) {
    if (depth > MAX_DEPTH) throw new ShellParseError("nesting too deep");
  }

  parse(): Script {
    const script = this.parseList();
    if (this.i < this.s.length) throw new ShellParseError(`unexpected "${this.s[this.i]}"`);
    return script;
  }

  private parseList(close?: string): Script {
    const s = this.s;
    const script: Script = { items: [] };
    let pipeline: Pipeline = { cmds: [] };
    let cmd: SimpleCommand | undefined;
    let continuation = false;
    const newCmd = (): SimpleCommand => ({ assigns: [], words: [], redirects: [] });
    const endCmd = () => {
      if (cmd && (cmd.words.length || cmd.assigns.length || cmd.redirects.length || cmd.group)) pipeline.cmds.push(cmd);
      cmd = undefined;
    };
    const endPipeline = (sep: string) => {
      endCmd();
      if (pipeline.cmds.length) script.items.push({ pipeline, sep });
      pipeline = { cmds: [] };
    };

    while (this.i < s.length) {
      const c = s[this.i]!;
      if (c === " " || c === "\t" || c === "\r") {
        this.i++;
        continue;
      }
      if (c === "\\" && s[this.i + 1] === "\n") {
        this.i += 2;
        continue;
      }
      if (c === "\n") {
        this.i++;
        this.readHeredocs();
        if (continuation) continue;
        endPipeline("\n");
        continue;
      }
      if (c === "#") {
        while (this.i < s.length && s[this.i] !== "\n") this.i++;
        continue;
      }
      if (c === ")") {
        if (close !== ")") throw new ShellParseError('unexpected ")"');
        this.i++;
        endPipeline("");
        return script;
      }
      continuation = false;
      if (c === ";") {
        this.i += s[this.i + 1] === ";" ? 2 : 1;
        endPipeline(";");
        continue;
      }
      if (c === "&") {
        if (s[this.i + 1] === "&") {
          this.i += 2;
          endPipeline("&&");
          continuation = true;
          continue;
        }
        if (s[this.i + 1] === ">") {
          cmd ??= newCmd();
          cmd.redirects.push(this.parseRedirect());
          continue;
        }
        this.i++;
        endPipeline("&");
        continue;
      }
      if (c === "|") {
        if (s[this.i + 1] === "|") {
          this.i += 2;
          endPipeline("||");
        } else {
          this.i += s[this.i + 1] === "&" ? 2 : 1;
          endCmd();
        }
        continuation = true;
        continue;
      }
      if (c === "(") {
        if (cmd && cmd.words.length) throw new ShellParseError('unexpected "("');
        this.i++;
        const inner = this.nested(() => this.parseList(")"));
        cmd ??= newCmd();
        cmd.group = inner;
        continue;
      }
      if ((c === "<" || c === ">") && s[this.i + 1] === "(") {
        this.i += 2;
        const inner = this.nested(() => this.parseList(")"));
        cmd ??= newCmd();
        cmd.words.push({ text: `${c}(…)`, quoted: false, dynamic: true, subs: [], procSubs: [inner] });
        continue;
      }
      if (c === "<" || c === ">") {
        cmd ??= newCmd();
        cmd.redirects.push(this.parseRedirect());
        continue;
      }
      const w = this.parseWord();
      cmd ??= newCmd();
      if (!w.quoted && /^\d+$/.test(w.text) && (s[this.i] === "<" || s[this.i] === ">")) {
        cmd.redirects.push(this.parseRedirect(w.text));
      } else if (cmd.words.length === 0 && !cmd.group && ASSIGN_RE.test(w.text)) {
        cmd.assigns.push(w);
      } else {
        cmd.words.push(w);
      }
    }
    if (close) throw new ShellParseError(`missing "${close}"`);
    endPipeline("");
    return script;
  }

  private nested<T>(fn: () => T): T {
    if (this.depth + 1 > MAX_DEPTH) throw new ShellParseError("nesting too deep");
    return fn();
  }

  private parseRedirect(fd?: string): Redirect {
    const s = this.s;
    const ops = ["<<<", "<<-", "<<", "<&", "<>", "&>>", "&>", ">>", ">&", ">|", "<", ">"];
    const op = ops.find((o) => s.startsWith(o, this.i));
    if (!op) throw new ShellParseError("bad redirection");
    this.i += op.length;
    while (s[this.i] === " " || s[this.i] === "\t") this.i++;
    const target = this.parseWord();
    if (!target.text && !target.quoted) throw new ShellParseError("missing redirection target");
    const r: Redirect = { op, target, ...(fd ? { fd } : {}) };
    if (op === "<<" || op === "<<-") this.pending.push(r);
    return r;
  }

  private readHeredocs(): void {
    const s = this.s;
    for (const r of this.pending) {
      const lines: string[] = [];
      for (;;) {
        if (this.i >= s.length) break;
        let nl = s.indexOf("\n", this.i);
        if (nl < 0) nl = s.length;
        const line = s.slice(this.i, nl);
        this.i = Math.min(nl + 1, s.length);
        const cmp = r.op === "<<-" ? line.replace(/^\t+/, "") : line;
        if (cmp === r.target.text) break;
        lines.push(line);
      }
      r.body = lines.join("\n");
    }
    this.pending = [];
  }

  private parseWord(): Word {
    const s = this.s;
    const w: Word = { text: "", quoted: false, dynamic: false, subs: [], procSubs: [] };
    while (this.i < s.length) {
      const c = s[this.i]!;
      if (WORD_BREAK.includes(c)) break;
      if (c === "\\") {
        if (s[this.i + 1] === "\n") {
          this.i += 2;
          continue;
        }
        w.text += s[this.i + 1] ?? "";
        w.quoted = true;
        this.i += 2;
        continue;
      }
      if (c === "'") {
        const end = s.indexOf("'", this.i + 1);
        if (end < 0) throw new ShellParseError("unterminated single quote");
        w.text += s.slice(this.i + 1, end);
        w.quoted = true;
        this.i = end + 1;
        continue;
      }
      if (c === '"') {
        this.i++;
        w.quoted = true;
        this.parseDoubleQuoted(w);
        continue;
      }
      if (c === "$" || c === "`") {
        this.parseDollar(w, false);
        continue;
      }
      w.text += c;
      this.i++;
    }
    return w;
  }

  private parseDoubleQuoted(w: Word): void {
    const s = this.s;
    for (;;) {
      if (this.i >= s.length) throw new ShellParseError("unterminated double quote");
      const c = s[this.i]!;
      if (c === '"') {
        this.i++;
        return;
      }
      if (c === "\\") {
        const n = s[this.i + 1];
        if (n === "\n") {
          this.i += 2;
          continue;
        }
        if (n !== undefined && '"\\$`'.includes(n)) {
          w.text += n;
          this.i += 2;
          continue;
        }
        w.text += c;
        this.i++;
        continue;
      }
      if (c === "$" || c === "`") {
        this.parseDollar(w, true);
        continue;
      }
      w.text += c;
      this.i++;
    }
  }

  /** `$(...)`, `$((...))`, `${...}`, `$NAME`, `$'...'`, backticks. */
  private parseDollar(w: Word, inDouble: boolean): void {
    const s = this.s;
    if (s[this.i] === "`") {
      let j = this.i + 1;
      let body = "";
      while (j < s.length && s[j] !== "`") {
        if (s[j] === "\\" && j + 1 < s.length) {
          body += s[j + 1];
          j += 2;
          continue;
        }
        body += s[j];
        j++;
      }
      if (j >= s.length) throw new ShellParseError("unterminated backtick");
      this.i = j + 1;
      w.subs.push(new Parser(body, this.depth + 1).parse());
      w.text += "$(…)";
      w.dynamic = true;
      return;
    }
    const n = s[this.i + 1];
    if (n === "(" && s[this.i + 2] === "(") {
      // arithmetic expansion: skip to the matching "))"
      let depth = 0;
      let j = this.i + 1;
      for (; j < s.length; j++) {
        if (s[j] === "(") depth++;
        else if (s[j] === ")" && --depth === 0) break;
      }
      if (j >= s.length) throw new ShellParseError("unterminated arithmetic expansion");
      w.text += s.slice(this.i, j + 1);
      w.dynamic = true;
      this.i = j + 1;
      return;
    }
    if (n === "(") {
      this.i += 2;
      w.subs.push(this.nested(() => this.parseList(")")));
      w.text += "$(…)";
      w.dynamic = true;
      return;
    }
    if (n === "{") {
      const end = s.indexOf("}", this.i);
      if (end < 0) throw new ShellParseError("unterminated ${");
      w.text += s.slice(this.i, end + 1);
      w.dynamic = true;
      this.i = end + 1;
      return;
    }
    if (n === "'" && !inDouble) {
      let j = this.i + 2;
      let body = "";
      while (j < s.length && s[j] !== "'") {
        if (s[j] === "\\" && j + 1 < s.length) {
          const e = s[j + 1]!;
          body += e === "n" ? "\n" : e === "t" ? "\t" : e;
          j += 2;
          continue;
        }
        body += s[j];
        j++;
      }
      if (j >= s.length) throw new ShellParseError("unterminated $'");
      w.text += body;
      w.quoted = true;
      this.i = j + 1;
      return;
    }
    if (n !== undefined && /[A-Za-z_]/.test(n)) {
      const m = /^[A-Za-z_][A-Za-z0-9_]*/.exec(s.slice(this.i + 1))!;
      w.text += `$${m[0]}`;
      w.dynamic = true;
      this.i += 1 + m[0].length;
      return;
    }
    if (n !== undefined && /[0-9@*#?$!-]/.test(n)) {
      w.text += `$${n}`;
      w.dynamic = true;
      this.i += 2;
      return;
    }
    w.text += "$";
    this.i++;
  }
}

export function parseShell(command: string): Script {
  return new Parser(command).parse();
}

// ---------------------------------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------------------------------

/** Lexically normalizes an absolute (`/…`) or home-relative (`~…`) path. */
export function normalizePath(p: string): string {
  const home = p === "~" || p.startsWith("~/");
  const rest = home ? p.slice(1) : p;
  const out: string[] = [];
  let aboveHome = false;
  for (const seg of rest.split("/")) {
    if (!seg || seg === ".") continue;
    if (seg === "..") {
      if (out.length) out.pop();
      else if (home) aboveHome = true;
      continue;
    }
    out.push(seg);
  }
  // Above the (unknown) home directory: somewhere in /Users or /home, treat as the root.
  if (home && aboveHome) return "/";
  const joined = out.join("/");
  if (home) return joined ? `~/${joined}` : "~";
  return `/${joined}`;
}

/** Resolves a shell word to a path; undefined when it depends on unknown expansions. */
export function resolvePath(arg: string, cwd: string | undefined): string | undefined {
  let p = arg;
  p = p.replace(/^(\$HOME|\$\{HOME\})(?=\/|$)/, "~");
  if (p.includes("$(") || p.includes("$") || p.includes("`")) return undefined;
  if (p === "~" || p.startsWith("~/")) return normalizePath(p);
  if (p.startsWith("~")) return undefined; // ~user
  if (p.startsWith("/")) return normalizePath(p);
  if (!cwd) return undefined;
  return normalizePath(`${cwd}/${p}`);
}

function isUnder(path: string, root: string): boolean {
  if (root === "/") return path.startsWith("/");
  return path === root || path.startsWith(`${root}/`);
}

function isInside(path: string, roots: string[]): boolean {
  return roots.some((r) => isUnder(path, r));
}

const TEMP_ROOTS = ["/tmp", "/private/tmp", "/var/tmp", "/var/folders", "/private/var/folders", "/dev/shm"];

function isTempPath(path: string): boolean {
  return TEMP_ROOTS.some((r) => path.startsWith(`${r}/`) && path !== `${r}/*`);
}

const SYSTEM_ROOTS = [
  "/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/opt", "/proc", "/root", "/run", "/sbin",
  "/srv", "/sys", "/usr", "/var", "/System", "/Library", "/Applications", "/private", "/cores", "/snap", "/nix",
  "/Volumes", "/mnt", "/media", "/Users", "/home", "/tmp",
];

/** Paths whose recursive removal (or modification) is never routine: /, home, system directories. */
function isCriticalPath(path: string): boolean {
  let p = path;
  if (p.endsWith("/*") || p.endsWith("/.*")) p = p.slice(0, p.lastIndexOf("/")) || "/";
  if (p === "/*" || p === "/" || p === "~") return true;
  if (/^~\/\.[^/]/.test(p)) return true; // dot directories and files directly in home: ~/.ssh, ~/.config/...
  if (isTempPath(p)) return false;
  const segs = p.split("/").filter(Boolean);
  if (p.startsWith("~")) return false;
  const top = `/${segs[0] ?? ""}`;
  if (!SYSTEM_ROOTS.includes(top)) return false;
  if (top === "/Users" || top === "/home") {
    if (segs.length <= 2) return true; // /Users, /Users/alice
    return segs[2]!.startsWith("."); // /Users/alice/.ssh
  }
  if (top === "/Volumes" || top === "/mnt" || top === "/media") return segs.length <= 2;
  return true;
}

const SHELL_RC = [
  ".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile", ".zshrc", ".zprofile", ".zshenv", ".zlogin",
  ".zlogout", ".kshrc", ".cshrc", ".tcshrc", ".inputrc",
];

/** Writing here grants persistence or escalation: shell rc files, ssh config, cron, launchd/systemd, /etc. */
function isPrivilegedWritePath(path: string): boolean {
  const homeRel = path.startsWith("~/") ? path.slice(2) : /^\/(?:Users|home)\/[^/]+\/(.+)$/.exec(path)?.[1] ?? /^\/root\/(.+)$/.exec(path)?.[1];
  if (homeRel !== undefined) {
    if (SHELL_RC.includes(homeRel)) return true;
    if (/^\.config\/fish\/(config\.fish|conf\.d\/)/.test(homeRel)) return true;
    if (/^\.ssh\/(config|authorized_keys2?|rc)$/.test(homeRel)) return true;
    if (/^Library\/LaunchAgents\//.test(homeRel)) return true;
    if (/^\.config\/(systemd|autostart)\//.test(homeRel)) return true;
  }
  if (/^\/(private\/)?etc\//.test(path)) return true;
  if (/^\/(private\/)?var\/(spool\/cron|at)\//.test(path)) return true;
  if (/^\/Library\/(LaunchAgents|LaunchDaemons)\//.test(path)) return true;
  if (/^\/usr\/(local\/)?(s?bin|lib)\//.test(path) || /^\/s?bin\//.test(path)) return true;
  return false;
}

/** Raw block devices (not /dev/null, /dev/tty, ...). */
function isBlockDevice(path: string): boolean {
  return /^\/dev\/(r?disk\d|sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|md\d|dm-\d|loop\d|mapper\/)/.test(path);
}

const SECRET_PATTERNS: RegExp[] = [
  /(^|\/)\.aws\/(credentials|config)$/,
  /(^|\/)\.ssh\/(id_[^/]*|identity)$/,
  /(^|\/)\.env(\.[^/]*)?$/,
  /(^|\/)\.envrc$/,
  /(^|\/)[._]netrc$/,
  /(^|\/)\.docker\/config\.json$/,
  /(^|\/)\.git-credentials$/,
  /(^|\/)\.npmrc$/,
  /(^|\/)\.pypirc$/,
  /(^|\/)\.kube\/config$/,
  /(^|\/)\.config\/gcloud(\/|$)/,
  /(^|\/)\.config\/gh\/hosts\.yml$/,
  /(^|\/)\.azure(\/|$)/,
  /(^|\/)\.gnupg(\/|$)/,
  /(^|\/)\.password-store(\/|$)/,
  /(^|\/)Keychains(\/|$)/,
  /^\/etc\/(shadow|gshadow|master\.passwd)$/,
  /^\/proc\/[^/]+\/environ$/,
];

function isSecretPath(s: string): boolean {
  if (/\.pub$/.test(s)) return false;
  if (/(^|\/)\.env\.(example|sample|template|dist|defaults)$/.test(s)) return false;
  return SECRET_PATTERNS.some((re) => re.test(s));
}

/** A word mentions a credential file: the word itself or the part after `@` / `=` (curl -d @file, -F f=@file). */
function mentionsSecret(word: string): boolean {
  const candidates = new Set([word, word.slice(word.lastIndexOf("@") + 1), word.slice(word.lastIndexOf("=") + 1)]);
  for (const c of candidates) {
    const v = c.replace(/^(\$HOME|\$\{HOME\})(?=\/)/, "~");
    if (v && isSecretPath(v)) return true;
  }
  return false;
}

function globToRegExp(glob: string): RegExp {
  const esc = glob.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".");
  return new RegExp(`^${esc}$`, "i");
}

// ---------------------------------------------------------------------------------------------------
// Command model
// ---------------------------------------------------------------------------------------------------

interface Cmd {
  /** argv after wrapper stripping; argv[0] is the program. */
  argv: string[];
  name: string;
  /** Words matching argv (same length), for dynamic / substitution checks. */
  words: Word[];
  assigns: string[];
  redirects: Redirect[];
  /** Effective working directory (undefined when unknown). */
  cwd: string | undefined;
  /** Program run with elevated privileges (sudo, doas, su, pkexec). */
  escalated?: string;
  /** Arguments come from stdin (xargs) or a placeholder (find -exec): positionals are unknown. */
  unknownArgs?: boolean;
  /** Source text of the simple command (assignments and wrappers included). */
  text: string;
  /** Stage index inside its pipeline and the pipeline's commands. */
  stage: number;
  pipeline: Cmd[];
}

function base(p: string): string {
  const i = p.lastIndexOf("/");
  return i >= 0 ? p.slice(i + 1) : p;
}

const KEYWORDS = new Set(["if", "then", "else", "elif", "do", "while", "until", "!", "{"]);
const NEUTRAL = new Set(["", "fi", "done", "esac", "}", "for", "select", "in", ":", "true", "false"]);
const SHELLS = new Set(["sh", "bash", "zsh", "dash", "ksh", "mksh", "ash", "fish", "busybox"]);
const ESCALATORS = new Set(["sudo", "doas", "pkexec", "run0"]);

interface Unwrapped {
  argv: string[];
  words: Word[];
  escalated?: string;
  unknownArgs?: boolean;
  /** Inline scripts to analyze recursively (bash -c, eval, su -c, find -exec). */
  inline: string[];
}

function skipOptions(argv: string[], withValue: Set<string>): number {
  let i = 0;
  while (i < argv.length) {
    const a = argv[i]!;
    if (a === "--") return i + 1;
    if (!a.startsWith("-") || a === "-") return i;
    i += withValue.has(a) ? 2 : 1;
  }
  return i;
}

function unwrap(argvIn: string[], wordsIn: Word[]): Unwrapped {
  let argv = argvIn;
  let words = wordsIn;
  let escalated: string | undefined;
  let unknownArgs = false;
  const inline: string[] = [];
  const cut = (n: number) => {
    argv = argv.slice(n);
    words = words.slice(n);
  };
  for (let guard = 0; guard < 16 && argv.length; guard++) {
    const a0 = argv[0]!;
    const name = base(a0);
    if (KEYWORDS.has(a0)) {
      cut(1);
      continue;
    }
    if (ASSIGN_RE.test(a0) && guard > 0) {
      cut(1);
      continue;
    }
    if (ESCALATORS.has(name)) {
      escalated = name;
      cut(1);
      cut(skipOptions(argv, new Set(["-u", "-g", "-C", "-h", "-p", "-r", "-t", "-U", "-D", "-R", "-T"])));
      continue;
    }
    if (name === "su") {
      escalated = "su";
      const ci = argv.indexOf("-c");
      if (ci >= 0 && argv[ci + 1] !== undefined) inline.push(argv[ci + 1]!);
      return { argv: [], words: [], escalated, unknownArgs, inline };
    }
    switch (name) {
      case "env": {
        cut(1);
        while (argv.length && (argv[0]!.startsWith("-") || ASSIGN_RE.test(argv[0]!))) {
          const opt = argv[0]!;
          cut(["-u", "-C", "-S", "--unset", "--chdir"].includes(opt) ? 2 : 1);
        }
        continue;
      }
      case "command":
      case "builtin":
        if (argv[1] === "-v" || argv[1] === "-V") return { argv, words, escalated, unknownArgs, inline };
        cut(1);
        cut(skipOptions(argv, new Set()));
        continue;
      case "exec":
      case "nohup":
      case "noglob":
      case "chronic":
      case "unbuffer":
      case "caffeinate":
        cut(1);
        cut(skipOptions(argv, new Set(["-a", "-t", "-w"])));
        continue;
      case "time":
        cut(1);
        cut(skipOptions(argv, new Set(["-o", "-f"])));
        continue;
      case "nice":
        cut(1);
        cut(skipOptions(argv, new Set(["-n", "--adjustment"])));
        continue;
      case "ionice":
        cut(1);
        cut(skipOptions(argv, new Set(["-c", "-n", "-p"])));
        continue;
      case "stdbuf":
        cut(1);
        cut(skipOptions(argv, new Set(["-i", "-o", "-e"])));
        continue;
      case "timeout":
      case "gtimeout": {
        cut(1);
        cut(skipOptions(argv, new Set(["-s", "-k", "--signal", "--kill-after"])));
        cut(1); // duration
        continue;
      }
      case "watch":
        cut(1);
        cut(skipOptions(argv, new Set(["-n", "--interval", "-d"])));
        if (argv.length > 1) {
          inline.push(argv.join(" "));
          return { argv: [], words: [], escalated, unknownArgs, inline };
        }
        continue;
      case "xargs": {
        cut(1);
        cut(skipOptions(argv, new Set(["-I", "-i", "-n", "-P", "-L", "-l", "-d", "-E", "-e", "-s", "-a", "--max-args", "--max-procs", "--delimiter", "--arg-file"])));
        unknownArgs = true;
        if (!argv.length) return { argv: ["echo"], words: [w("echo")], escalated, unknownArgs, inline };
        continue;
      }
      case "eval":
        inline.push(argv.slice(1).join(" "));
        return { argv, words, escalated, unknownArgs, inline };
      default:
        break;
    }
    if (SHELLS.has(name)) {
      const ci = argv.findIndex((x, i) => i > 0 && /^-[a-zA-Z]*c[a-zA-Z]*$/.test(x));
      if (ci >= 0 && argv[ci + 1] !== undefined) {
        inline.push(argv[ci + 1]!);
        return { argv, words, escalated, unknownArgs, inline };
      }
    }
    break;
  }
  return { argv, words, escalated, unknownArgs, inline };
}

function w(text: string): Word {
  return { text, quoted: false, dynamic: false, subs: [], procSubs: [] };
}

function cmdText(sc: SimpleCommand): string {
  return [...sc.assigns.map((a) => a.text), ...sc.words.map((x) => x.text)].join(" ");
}

/** Walks a script, expanding nested scripts (subshells, substitutions, bash -c bodies) into simple commands. */
function flatten(script: Script, cwd: string | undefined, out: Cmd[], depth = 0): string | undefined {
  if (depth > MAX_DEPTH) throw new ShellParseError("nesting too deep");
  let cur = cwd;
  for (const item of script.items) {
    const stageCmds: Cmd[] = [];
    for (const [stage, sc] of item.pipeline.cmds.entries()) {
      for (const a of sc.assigns) for (const sub of a.subs) flatten(sub, cur, out, depth + 1);
      for (const word of sc.words) {
        for (const sub of word.subs) flatten(sub, cur, out, depth + 1);
        for (const sub of word.procSubs) flatten(sub, cur, out, depth + 1);
      }
      for (const r of sc.redirects) for (const sub of r.target.subs) flatten(sub, cur, out, depth + 1);
      if (sc.group) {
        flatten(sc.group, cur, out, depth + 1);
        continue;
      }
      const u = unwrap(
        sc.words.map((x) => x.text),
        sc.words,
      );
      for (const body of u.inline) flatten(parseShell(body), cur, out, depth + 1);
      const name = u.argv.length ? base(u.argv[0]!) : "";
      const cmd: Cmd = {
        argv: u.argv,
        name,
        words: u.words,
        assigns: sc.assigns.map((a) => a.text),
        redirects: sc.redirects,
        cwd: cur,
        text: cmdText(sc),
        stage,
        pipeline: stageCmds,
        ...(u.escalated ? { escalated: u.escalated } : {}),
        ...(u.unknownArgs ? { unknownArgs: true } : {}),
      };
      // A shell reading a heredoc runs its body.
      if (SHELLS.has(name) && u.inline.length === 0) {
        for (const r of sc.redirects) if (r.body !== undefined) flatten(parseShell(r.body), cur, out, depth + 1);
      }
      // find -exec / -execdir / -ok run a command with placeholder arguments.
      if (name === "find" || name === "fd") {
        const idx = u.argv.findIndex((x) => ["-exec", "-execdir", "-ok", "-okdir", "-x", "--exec", "-X", "--exec-batch"].includes(x));
        if (idx >= 0) {
          let end = u.argv.findIndex((x, i) => i > idx && (x === ";" || x === "+" || x === "\\;"));
          if (end < 0) end = u.argv.length;
          const sub = u.argv.slice(idx + 1, end);
          if (sub.length) {
            const su = unwrap(sub, sub.map(w));
            out.push({
              argv: su.argv,
              name: su.argv.length ? base(su.argv[0]!) : "",
              words: su.words,
              assigns: [],
              redirects: [],
              cwd: cur,
              text: sub.join(" "),
              stage: 0,
              pipeline: [],
              unknownArgs: true,
              ...(su.escalated ? { escalated: su.escalated } : {}),
            });
            for (const body of su.inline) flatten(parseShell(body), cur, out, depth + 1);
          }
        }
      }
      stageCmds.push(cmd);
      out.push(cmd);
      // cd / pushd change the directory for the rest of the list (not across a pipeline).
      if ((name === "cd" || name === "pushd") && item.pipeline.cmds.length === 1) {
        const target = u.argv[1];
        if (target === undefined) cur = "~";
        else if (target === "-") cur = undefined;
        else cur = resolvePath(target, cur);
      }
    }
  }
  return cur;
}

// ---------------------------------------------------------------------------------------------------
// Option helpers
// ---------------------------------------------------------------------------------------------------

interface ParsedArgs {
  shorts: Set<string>;
  longs: Map<string, string | true>;
  positionals: string[];
  /** Option values by option (for options listed in withValue). */
  values: Map<string, string[]>;
}

function parseArgs(args: string[], withValue: string[] = []): ParsedArgs {
  const shorts = new Set<string>();
  const longs = new Map<string, string | true>();
  const positionals: string[] = [];
  const values = new Map<string, string[]>();
  const wv = new Set(withValue);
  const addValue = (k: string, v: string) => values.set(k, [...(values.get(k) ?? []), v]);
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!;
    if (a === "--") {
      positionals.push(...args.slice(i + 1));
      break;
    }
    if (a.startsWith("--") && a.length > 2) {
      const eq = a.indexOf("=");
      const k = eq >= 0 ? a.slice(0, eq) : a;
      if (eq >= 0) {
        longs.set(k, a.slice(eq + 1));
        addValue(k, a.slice(eq + 1));
      } else if (wv.has(k) && i + 1 < args.length) {
        longs.set(k, args[i + 1]!);
        addValue(k, args[++i]!);
      } else longs.set(k, true);
      continue;
    }
    if (a.startsWith("-") && a.length > 1) {
      if (wv.has(a) && i + 1 < args.length) {
        addValue(a, args[++i]!);
        shorts.add(a.slice(1));
        continue;
      }
      if (/^-[A-Za-z]+$/.test(a)) for (const ch of a.slice(1)) shorts.add(ch);
      else shorts.add(a.slice(1));
      continue;
    }
    positionals.push(a);
  }
  return { shorts, longs, positionals, values };
}

function hasFlag(argv: string[], ...flags: string[]): boolean {
  return argv.some((a) => flags.some((f) => a === f || (f.startsWith("--") && a.startsWith(`${f}=`))));
}

// ---------------------------------------------------------------------------------------------------
// Rule context and hits
// ---------------------------------------------------------------------------------------------------

interface Hit {
  rule: string;
  reason: string;
  categories: RuleCategory[];
}

interface Ctx {
  roots: string[];
  cwd: string | undefined;
  facts: RulesFacts;
  protectedBranches: RegExp[];
  denyAllForcePush: boolean;
}

const FETCHERS = new Set(["curl", "wget", "fetch", "http", "https", "xh", "httpie", "lwp-request", "aria2c"]);
const NETWORK_TOOLS = new Set([
  ...FETCHERS, "nc", "ncat", "netcat", "socat", "telnet", "ftp", "sftp", "scp", "ssh", "rsync", "openssl", "gh", "aws", "gsutil",
  "gcloud", "az", "rclone", "s3cmd", "mail", "sendmail", "mutt", "dig", "nslookup", "host", "ping", "smbclient", "tftp",
]);

function isInterpreter(name: string): boolean {
  return SHELLS.has(name) || /^python[0-9.]*$/.test(name) || /^(perl|ruby|node|nodejs|deno|bun|php|pwsh|powershell|lua|tclsh|osascript)$/.test(name);
}

/** The interpreter takes its program from stdin (so piping a download into it executes the download). */
function readsProgramFromStdin(c: Cmd): boolean {
  const args = c.argv.slice(1);
  const name = c.name;
  if (SHELLS.has(name)) {
    if (args.some((a) => /^-[a-zA-Z]*c[a-zA-Z]*$/.test(a))) return false;
    if (args.some((a) => /^-[a-zA-Z]*s[a-zA-Z]*$/.test(a) || a === "-")) return true;
    return !args.some((a) => !a.startsWith("-"));
  }
  if (/^python[0-9.]*$/.test(name)) {
    if (hasFlag(args, "-c", "-m")) return false;
    const pos = args.filter((a) => !a.startsWith("-") || a === "-");
    return pos.length === 0 || pos[0] === "-";
  }
  if (/^(perl|ruby|php|lua)$/.test(name)) {
    if (hasFlag(args, "-e", "-E", "-r")) return false;
    const pos = args.filter((a) => !a.startsWith("-") || a === "-");
    return pos.length === 0 || pos[0] === "-";
  }
  if (/^(node|nodejs|bun)$/.test(name)) {
    if (hasFlag(args, "-e", "--eval", "-p", "--print")) return false;
    const pos = args.filter((a) => !a.startsWith("-") || a === "-");
    return pos.length === 0 || pos[0] === "-";
  }
  if (name === "deno") return args.includes("-");
  if (name === "pwsh" || name === "powershell") return !hasFlag(args, "-c", "-Command", "-File", "-f");
  return false;
}

function containsFetcher(scripts: Script[], cwd: string | undefined): boolean {
  for (const s of scripts) {
    const cmds: Cmd[] = [];
    flatten(s, cwd, cmds, 1);
    if (cmds.some((c) => FETCHERS.has(c.name))) return true;
  }
  return false;
}

/** Positional targets of a command that writes (for privileged-path checks). */
function writeTargets(c: Cmd): string[] {
  const args = c.argv.slice(1);
  switch (c.name) {
    case "cp":
    case "mv":
    case "install":
    case "ln":
    case "rsync": {
      const p = parseArgs(args, ["-t", "--target-directory", "-m", "--mode", "-o", "-g", "-S", "--suffix", "-e"]);
      const t = p.values.get("-t") ?? p.values.get("--target-directory");
      if (t) return t;
      return p.positionals.length >= 2 ? [p.positionals[p.positionals.length - 1]!] : [];
    }
    case "tee":
    case "truncate":
    case "touch":
    case "rm":
    case "unlink":
    case "shred":
      return parseArgs(args, ["-s", "--size", "-r", "--reference", "-d", "-t", "-n"]).positionals;
    case "sed":
    case "gsed":
    case "perl": {
      if (!args.some((a) => /^-[a-zA-Z]*i/.test(a) || a.startsWith("--in-place"))) return [];
      const p = parseArgs(args, ["-e", "-f", "--expression", "--file"]);
      const scripted = p.values.has("-e") || p.values.has("--expression") || p.values.has("-f") || p.values.has("--file");
      return scripted ? p.positionals : p.positionals.slice(1);
    }
    case "dd":
      return args.filter((a) => a.startsWith("of=")).map((a) => a.slice(3));
    case "chmod":
    case "chown":
    case "chgrp":
      return parseArgs(args).positionals.slice(1);
    default:
      return [];
  }
}

const WRITE_OPS = new Set([">", ">>", ">|", "&>", "&>>", "<>"]);

function redirectWrites(c: Cmd): Redirect[] {
  return c.redirects.filter((r) => WRITE_OPS.has(r.op) || (r.op === ">&" && !/^(\d+|-)$/.test(r.target.text)));
}

// ---------------------------------------------------------------------------------------------------
// Hard-deny checks (per simple command)
// ---------------------------------------------------------------------------------------------------

const DESTRUCTIVE: RuleCategory[] = ["destructive"];

function checkEscalation(c: Cmd): Hit | undefined {
  if (c.escalated) {
    return { rule: "privilege.sudo", reason: `Runs a command with elevated privileges (${c.escalated})`, categories: ["privilege_escalation"] };
  }
  return undefined;
}

function checkRm(c: Cmd, ctx: Ctx): Hit | undefined {
  if (c.name !== "rm") return undefined;
  const p = parseArgs(c.argv.slice(1));
  if (p.longs.has("--no-preserve-root")) return { rule: "destructive.rm_root", reason: "Removes files with --no-preserve-root", categories: DESTRUCTIVE };
  const recursive = p.shorts.has("r") || p.shorts.has("R") || p.longs.has("--recursive");
  const targets: (string | undefined)[] = p.positionals.map((t) => resolvePath(t, c.cwd));
  if (recursive && ctx.facts.write_paths?.length) targets.push(...ctx.facts.write_paths.map((x) => resolvePath(x, undefined)));
  const safeRoots = ctx.roots.filter((r) => !isCriticalPath(r));
  for (const t of targets) {
    if (t === undefined) continue;
    const bare = t.endsWith("/*") ? t.slice(0, -2) || "/" : t;
    if (recursive && ctx.roots.includes(bare)) {
      return { rule: "destructive.rm_workspace", reason: "Recursively removes the whole working directory", categories: DESTRUCTIVE };
    }
    if (isInside(t, safeRoots)) continue;
    if (isCriticalPath(t)) {
      return { rule: "destructive.rm_critical", reason: `Removes ${t === "~" ? "the home directory" : t}, a system or home path`, categories: DESTRUCTIVE };
    }
    if (recursive && ctx.roots.length && !isInside(t, ctx.roots) && !isTempPath(t) && !t.startsWith("~")) {
      return { rule: "destructive.rm_outside", reason: `Recursively removes ${t}, outside the working directories`, categories: DESTRUCTIVE };
    }
  }
  return undefined;
}

function checkFindDelete(c: Cmd, ctx: Ctx): Hit | undefined {
  if (c.name !== "find") return undefined;
  const args = c.argv.slice(1);
  const deletes = args.includes("-delete") || args.some((a, i) => ["-exec", "-execdir"].includes(a) && ["rm", "shred"].includes(base(args[i + 1] ?? "")));
  if (!deletes) return undefined;
  const roots = [];
  for (const a of args) {
    if (a.startsWith("-") || a === "!" || a === "(") break;
    roots.push(a);
  }
  for (const r of roots) {
    const t = resolvePath(r, c.cwd);
    if (t && !isInside(t, ctx.roots.filter((x) => !isCriticalPath(x))) && isCriticalPath(t)) return { rule: "destructive.find_delete", reason: `Deletes files found under ${t}`, categories: DESTRUCTIVE };
  }
  return undefined;
}

function checkDevices(c: Cmd): Hit | undefined {
  const n = c.name;
  if (/^(mkfs(\..+)?|mke2fs|mkswap|newfs(_.+)?|wipefs|sfdisk|fdisk|gdisk|sgdisk|parted|cfdisk|blkdiscard)$/.test(n)) {
    return { rule: "destructive.disk_format", reason: `Formats or repartitions a disk (${n})`, categories: DESTRUCTIVE };
  }
  const dsub = c.argv[1] ?? "";
  if (n === "diskutil" && (/^(erase\w*|zeroDisk|randomDisk|secureErase|partitionDisk|reformat)$/i.test(dsub) || (dsub === "apfs" && /^delete/i.test(c.argv[2] ?? "")))) {
    return { rule: "destructive.disk_format", reason: "Erases or repartitions a disk (diskutil)", categories: DESTRUCTIVE };
  }
  const targets = [...writeTargets(c), ...redirectWrites(c).map((r) => r.target.text)];
  if (n === "shred" || n === "dd" || n === "cp" || n === "cat" || n === "tee" || redirectWrites(c).length) {
    for (const t of targets) {
      const p = resolvePath(t, c.cwd);
      if (p && isBlockDevice(p)) return { rule: "destructive.device_write", reason: `Writes directly to the block device ${p}`, categories: DESTRUCTIVE };
    }
  }
  return undefined;
}

function checkPrivilegedWrites(c: Cmd): Hit | undefined {
  if (c.name === "crontab") {
    if (parseArgs(c.argv.slice(1), ["-u"]).shorts.has("r")) {
      return { rule: "destructive.crontab_remove", reason: "Removes the whole crontab (crontab -r)", categories: DESTRUCTIVE };
    }
    const args = c.argv.slice(1).filter((a) => !["-u"].includes(a));
    if (!(args.length === 1 && args[0] === "-l")) {
      return { rule: "privilege.crontab", reason: "Installs or edits a crontab", categories: ["privilege_escalation"] };
    }
  }
  // Deletions are judged by checkRm (destructive), not as edits.
  const removes = ["rm", "unlink", "shred"].includes(c.name);
  const targets = [...(removes ? [] : writeTargets(c)), ...redirectWrites(c).map((r) => r.target.text)];
  for (const t of targets) {
    const p = resolvePath(t, c.cwd);
    if (p && isPrivilegedWritePath(p) && !isBlockDevice(p)) {
      return { rule: "privilege.protected_write", reason: `Modifies ${p} (shell startup, ssh, scheduler or system file)`, categories: ["privilege_escalation"] };
    }
  }
  return undefined;
}

/** Modes that set the setuid or setgid bit: u+s, g+s, +s, 4xxx, 2xxx, 6xxx. */
function isSetidMode(mode: string): boolean {
  const num = /^0*([0-7]{3,4})$/.exec(mode)?.[1];
  if (num) return num.length === 4 && (Number(num[0]) & 6) !== 0;
  return mode.split(",").some((part) => /^[ugoa]*[+=][rwxXt]*s/.test(part));
}

/** World-writable or setuid/setgid modes. */
function isWideMode(mode: string): boolean {
  const num = /^0*([0-7]{3,4})$/.exec(mode)?.[1];
  if (num) {
    const others = Number(num[num.length - 1]);
    const special = num.length === 4 ? Number(num[0]) : 0;
    return (others & 2) !== 0 || (special & 6) !== 0;
  }
  return mode.split(",").some((part) => /^[ugoa]*[oa][ugoa]*[+=][rwxXst]*w/.test(part) || /^[+=][rwxXst]*w/.test(part) || /^[ugoa]*[+=][rwxXt]*s/.test(part));
}

function checkChmod(c: Cmd, ctx: Ctx): Hit | undefined {
  if (!["chmod", "chown", "chgrp"].includes(c.name)) return undefined;
  const p = parseArgs(c.argv.slice(1));
  const recursive = p.shorts.has("R") || p.longs.has("--recursive");
  const [mode, ...targets] = p.positionals;
  if (!mode) return undefined;
  // setuid/setgid binaries are an escalation path wherever they live, the workspace included.
  if (c.name === "chmod" && targets.length && isSetidMode(mode)) {
    return { rule: "privilege.chmod_setuid", reason: `Sets the setuid or setgid bit (chmod ${mode})`, categories: ["privilege_escalation"] };
  }
  const wide = c.name === "chmod" && isWideMode(mode);
  const safeRoots = ctx.roots.filter((r) => !isCriticalPath(r));
  for (const t of targets) {
    const r = resolvePath(t, c.cwd);
    if (!r) continue;
    if (recursive && !isInside(r, safeRoots) && isCriticalPath(r)) {
      return { rule: "privilege.chmod_system", reason: `Recursively changes ownership or permissions of ${r}`, categories: ["privilege_escalation"] };
    }
    if (wide && !isInside(r, ctx.roots) && !isTempPath(r)) {
      return { rule: "privilege.chmod_wide", reason: `Makes ${r} world-writable or setuid outside the working directories`, categories: ["privilege_escalation"] };
    }
  }
  return undefined;
}

const GIT_GLOBAL_WITH_VALUE = new Set(["-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env"]);

function gitSub(argv: string[]): { sub: string; args: string[]; globals: string[] } {
  let i = 1;
  const globals: string[] = [];
  while (i < argv.length) {
    const a = argv[i]!;
    if (GIT_GLOBAL_WITH_VALUE.has(a)) {
      globals.push(a, argv[i + 1] ?? "");
      i += 2;
      continue;
    }
    if (a.startsWith("-")) {
      globals.push(a);
      i++;
      continue;
    }
    break;
  }
  return { sub: argv[i] ?? "", args: argv.slice(i + 1), globals };
}

const PROD_RE = /(^|[^a-z0-9])(prod|production|prd)([^a-z0-9]|$)/i;

function checkGit(c: Cmd, ctx: Ctx): Hit | undefined {
  if (c.name !== "git") return undefined;
  const { sub, args } = gitSub(c.argv);
  const protectedBranch = (b: string) => ctx.protectedBranches.some((re) => re.test(b));
  if (sub === "push") {
    const p = parseArgs(args, ["-o", "--push-option", "--receive-pack", "--exec", "--repo"]);
    const force =
      p.shorts.has("f") || p.longs.has("--force") || p.longs.has("--force-with-lease") || p.longs.has("--mirror") ||
      p.positionals.slice(1).some((r) => r.startsWith("+"));
    const del = p.shorts.has("d") || p.longs.has("--delete");
    const [remote, ...refspecs] = p.positionals;
    const current = ctx.facts.git_branch || undefined;
    let targets: (string | undefined)[] = refspecs.map((r) => {
      const s = r.replace(/^\+/, "");
      const dst = s.includes(":") ? s.slice(s.indexOf(":") + 1) : s;
      const b = dst.replace(/^refs\/heads\//, "");
      return b === "HEAD" ? current : b;
    });
    if (refspecs.length === 0) targets = p.longs.has("--all") || p.longs.has("--mirror") ? ["*all*"] : [current];
    if (remote && PROD_RE.test(remote) && !remote.includes("/") && !remote.includes(":")) {
      return { rule: "production.git_push", reason: `Pushes to the "${remote}" remote, which looks like a production deploy`, categories: ["production_change"] };
    }
    if (force) {
      if (ctx.denyAllForcePush) return { rule: "git.force_push", reason: "Force-pushes, which rewrites remote history", categories: ["git_history_rewrite"] };
      const hit = targets.find((t) => t === "*all*" || (t !== undefined && protectedBranch(t)));
      if (hit) {
        return {
          rule: "git.force_push",
          reason: hit === "*all*" ? "Force-pushes every branch" : `Force-pushes to the shared branch ${hit}`,
          categories: ["git_history_rewrite"],
        };
      }
    }
    const deletes = del ? targets : refspecs.filter((r) => r.startsWith(":")).map((r) => r.slice(1).replace(/^refs\/heads\//, ""));
    const delHit = (del || deletes.length) && deletes.find((t) => t !== undefined && protectedBranch(t));
    if (delHit) return { rule: "git.delete_branch", reason: `Deletes the shared remote branch ${delHit}`, categories: ["git_history_rewrite"] };
    return undefined;
  }
  if (sub === "reset" && args.includes("--hard")) {
    const ref = args.find((a) => !a.startsWith("-"));
    if (!ref) return undefined;
    const m = /^(?:refs\/remotes\/)?(origin|upstream)\/(.+)$/.exec(ref);
    const upstreamRef = /@\{(u|upstream|push)\}$/.test(ref);
    if (!m && !upstreamRef) return undefined;
    const target = m?.[2];
    const current = ctx.facts.git_branch;
    if ((target && protectedBranch(target)) || (current && protectedBranch(current))) {
      return { rule: "git.reset_shared", reason: `Hard-resets the shared branch ${current && protectedBranch(current) ? current : target}`, categories: ["git_history_rewrite"] };
    }
  }
  return undefined;
}

function kubeContext(argv: string[]): string | undefined {
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]!;
    if (a === "--context" || a === "--kube-context") return argv[i + 1];
    if (a.startsWith("--context=") || a.startsWith("--kube-context=")) return a.slice(a.indexOf("=") + 1);
  }
  return undefined;
}

function isLocalKubeContext(ctxName: string): boolean {
  return /^(kind-|k3d-|minikube|docker-desktop|docker-for-desktop|rancher-desktop|colima|orbstack|microk8s|local|localhost|dev-local)/i.test(ctxName);
}

const KUBECTL_WITH_VALUE = new Set(["--context", "-n", "--namespace", "--kubeconfig", "-l", "--selector", "-f", "--filename", "-o", "--output", "--cluster", "--user", "-s", "--server", "--as", "-c", "--container"]);

function firstPositional(args: string[], withValue: Set<string>): { sub: string; rest: string[] } {
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!;
    if (withValue.has(a)) {
      i++;
      continue;
    }
    if (a.startsWith("-")) continue;
    return { sub: a, rest: args.slice(i + 1) };
  }
  return { sub: "", rest: [] };
}

const LOCAL_KUBE_SERVER = /^(https?:\/\/)?(localhost|127\.0\.0\.1|\[::1\])(:|\/|$)/;

/** The target cluster is clearly local: a local --context, or (without one) a loopback --server. */
function isLocalKube(kctx: string | undefined, server: string | undefined): boolean {
  if (kctx !== undefined) return isLocalKubeContext(kctx);
  return server !== undefined && LOCAL_KUBE_SERVER.test(server);
}

function isKubeDryRun(args: string[]): boolean {
  return args.some((a) => a === "--dry-run" || (a.startsWith("--dry-run=") && !["none", "false"].includes(a.slice(10))));
}

/** kubectl operations that take workloads or nodes out of service (undefined when the call is none of them). */
function kubeDisruptive(sub: string, rest: string[], args: string[]): string | undefined {
  if (isKubeDryRun(args)) return undefined;
  if (sub === "drain" || sub === "cordon" || sub === "taint") return sub;
  if (sub === "scale") {
    for (let i = 0; i < args.length; i++) {
      const a = args[i]!;
      const v = a === "--replicas" ? args[i + 1] : a.startsWith("--replicas=") ? a.slice(11) : undefined;
      if (v !== undefined && /^0+$/.test(v)) return "scale --replicas=0";
    }
  }
  if (sub === "replace" && hasFlag(args, "--force")) return "replace --force";
  if (sub === "delete" && (hasFlag(args, "--all", "--all-namespaces") || args.includes("-A"))) return "delete --all";
  if (sub === "rollout" && firstPositional(rest, KUBECTL_WITH_VALUE).sub === "undo") return "rollout undo";
  return undefined;
}

const KUBE_MUTATING = new Set(["apply", "create", "replace", "patch", "scale", "rollout", "set", "edit", "drain", "cordon", "label", "annotate", "delete", "expose", "autoscale", "taint"]);

function checkInfra(c: Cmd): Hit | undefined {
  const n = c.name;
  const args = c.argv.slice(1);
  if (n === "terraform" || n === "tofu" || n === "terragrunt") {
    const { sub } = firstPositional(args, new Set());
    if (sub === "destroy" || (sub === "apply" && hasFlag(args, "-destroy", "--destroy"))) {
      return { rule: "production.terraform_destroy", reason: `Destroys infrastructure (${n} destroy)`, categories: ["destructive", "production_change"] };
    }
    if (sub === "apply" && hasFlag(args, "-auto-approve", "--auto-approve")) {
      return { rule: "production.terraform_apply", reason: `Applies infrastructure changes without review (${n} apply -auto-approve)`, categories: ["production_change"] };
    }
  }
  if (n === "pulumi") {
    const { sub } = firstPositional(args, new Set(["-s", "--stack", "-C", "--cwd"]));
    if (sub === "destroy") return { rule: "production.pulumi_destroy", reason: "Destroys a Pulumi stack", categories: ["destructive", "production_change"] };
    if ((sub === "up" || sub === "update") && hasFlag(args, "--yes", "-y", "--skip-preview", "-f")) {
      return { rule: "production.pulumi_up", reason: "Applies Pulumi changes without review", categories: ["production_change"] };
    }
  }
  if (n === "kubectl" || n === "oc") {
    const { sub, rest } = firstPositional(args, KUBECTL_WITH_VALUE);
    const kctx = kubeContext(args);
    const ns = (() => {
      const i = args.findIndex((a) => a === "-n" || a === "--namespace");
      return i >= 0 ? args[i + 1] : args.find((a) => a.startsWith("--namespace="))?.slice(12);
    })();
    const server = (() => {
      const i = args.findIndex((a) => a === "-s" || a === "--server");
      return i >= 0 ? args[i + 1] : args.find((a) => a.startsWith("--server="))?.slice(9);
    })();
    if (sub === "delete") {
      const remoteServer = server !== undefined && !LOCAL_KUBE_SERVER.test(server);
      if ((kctx !== undefined && !isLocalKubeContext(kctx)) || remoteServer) {
        return { rule: "production.kubectl_delete", reason: `Deletes Kubernetes resources in the non-local context ${kctx ?? server}`, categories: ["destructive", "production_change"] };
      }
    }
    const disruptive = kubeDisruptive(sub, rest, args);
    if (disruptive && !isLocalKube(kctx, server)) {
      const where = kctx !== undefined ? `the context ${kctx}` : server !== undefined ? `the server ${server}` : "the current context";
      return {
        rule: "production.kubectl_disruptive",
        reason: `Runs kubectl ${disruptive} against ${where}, which is not a local cluster`,
        categories: sub === "delete" ? ["destructive", "production_change"] : ["production_change"],
      };
    }
    if (KUBE_MUTATING.has(sub) && ((kctx && PROD_RE.test(kctx)) || (ns && PROD_RE.test(ns)))) {
      return { rule: "production.kubectl_prod", reason: `Changes Kubernetes resources in production (${kctx ?? ns})`, categories: ["production_change"] };
    }
  }
  if (n === "helm") {
    const { sub } = firstPositional(args, new Set(["-n", "--namespace", "--kube-context", "--kubeconfig"]));
    if (["uninstall", "delete", "del", "un"].includes(sub)) {
      return { rule: "production.helm_uninstall", reason: "Uninstalls a Helm release", categories: ["destructive", "production_change"] };
    }
  }
  return undefined;
}

const AWS_WITH_VALUE = new Set(["--profile", "--region", "--output", "--endpoint-url", "--query", "--color", "--ca-bundle", "--cli-read-timeout", "--cli-connect-timeout", "--cli-binary-format"]);
const AWS_DELETE_OPS = new Set([
  "terminate-instances", "delete-db-instance", "delete-db-cluster", "delete-stack", "delete-table", "delete-function", "delete-cluster",
  "delete-nodegroup", "delete-bucket", "delete-repository", "delete-hosted-zone", "delete-secret", "delete-file-system",
  "delete-load-balancer", "delete-vpc", "delete-log-group", "delete-queue", "delete-topic", "delete-service", "schedule-key-deletion",
]);
/** gcloud / az command groups that only touch local configuration. */
const GCLOUD_LOCAL_GROUPS = new Set(["config", "auth", "components", "topic", "help", "info"]);
const AZ_LOCAL_GROUPS = new Set(["config", "extension", "cache", "account", "login", "logout", "cloud"]);
const GSUTIL_WITH_VALUE = new Set(["-o", "-h", "-p", "-u"]);
const HEROKU_WITH_VALUE = new Set(["-a", "--app", "-r", "--remote"]);
const VERCEL_WITH_VALUE = new Set(["--token", "-t", "--scope", "-S", "--cwd", "-A", "--local-config", "-Q", "--global-config"]);
const NETLIFY_WITH_VALUE = new Set(["--auth"]);

/** Deletes of cloud resources through the providers' CLIs (read and list verbs are not affected). */
function checkCloudDelete(c: Cmd): Hit | undefined {
  const n = c.name;
  const args = c.argv.slice(1);
  const pos = args.filter((a) => !a.startsWith("-"));
  const verbAt = (limit: number, verbs: string[]) => pos.slice(0, limit).findIndex((x) => verbs.includes(x));
  let what: string | undefined;
  if (n === "aws") {
    const { sub: service, rest } = firstPositional(args, AWS_WITH_VALUE);
    const { sub: op } = firstPositional(rest, AWS_WITH_VALUE);
    if (service === "s3" && ((op === "rm" && hasFlag(rest, "--recursive")) || (op === "rb" && hasFlag(rest, "--force")))) what = `aws s3 ${op}`;
    else if (AWS_DELETE_OPS.has(op) || (service === "iam" && op.startsWith("delete-"))) what = `aws ${service} ${op}`;
  } else if (n === "gcloud") {
    const i = verbAt(5, ["delete"]);
    if (i > 0 && !GCLOUD_LOCAL_GROUPS.has(pos[0]!)) what = `gcloud ${pos.slice(0, i + 1).join(" ")}`;
    else if (pos[0] === "storage" && pos[1] === "rm" && hasFlag(args, "-r", "-R", "--recursive")) what = "gcloud storage rm --recursive";
  } else if (n === "gsutil") {
    const { sub, rest } = firstPositional(args, GSUTIL_WITH_VALUE);
    const p = parseArgs(rest);
    if (sub === "rb") what = "gsutil rb";
    else if (sub === "rm" && (p.shorts.has("r") || p.shorts.has("R") || p.longs.has("--recursive"))) what = "gsutil rm -r";
  } else if (n === "az") {
    const i = verbAt(4, ["delete", "delete-batch", "purge"]);
    if (i > 0 && !AZ_LOCAL_GROUPS.has(pos[0]!)) what = `az ${pos.slice(0, i + 1).join(" ")}`;
  } else if (n === "doctl") {
    const i = verbAt(4, ["delete", "rm"]);
    if (i > 0 && pos[0] !== "auth") what = `doctl ${pos.slice(0, i + 1).join(" ")}`;
  } else if (n === "fly" || n === "flyctl") {
    const i = verbAt(3, ["destroy", "delete"]);
    if (i >= 0) what = `${n} ${pos.slice(0, i + 1).join(" ")}`;
  } else if (n === "heroku") {
    const { sub } = firstPositional(args, HEROKU_WITH_VALUE);
    if (/(^|:)destroy$/.test(sub) || sub === "pg:reset") what = `heroku ${sub}`;
  } else if (n === "vercel") {
    const { sub, rest } = firstPositional(args, VERCEL_WITH_VALUE);
    if (sub === "remove" || sub === "rm") what = `vercel ${sub}`;
    else if ((sub === "project" || sub === "projects") && ["rm", "remove"].includes(firstPositional(rest, VERCEL_WITH_VALUE).sub)) what = `vercel ${sub} remove`;
  } else if (n === "netlify") {
    if (firstPositional(args, NETLIFY_WITH_VALUE).sub === "sites:delete") what = "netlify sites:delete";
  }
  if (what === undefined) return undefined;
  return { rule: "production.cloud_delete", reason: `Deletes cloud resources (${what})`, categories: ["destructive", "production_change"] };
}

const SYSTEMCTL_WITH_VALUE = new Set(["-H", "--host", "-M", "--machine"]);
const SYSTEMCTL_POWER = new Set(["poweroff", "reboot", "halt", "kexec"]);

function checkShutdown(c: Cmd): Hit | undefined {
  const n = c.name;
  const args = c.argv.slice(1);
  if (args.length === 1 && ["--help", "-h", "--version"].includes(args[0]!)) return undefined;
  let what: string | undefined;
  if (n === "shutdown" || n === "reboot" || n === "halt" || n === "poweroff") {
    if (!(n === "shutdown" && args.includes("-c"))) what = n; // shutdown -c cancels a pending shutdown
  } else if ((n === "init" || n === "telinit") && (args[0] === "0" || args[0] === "6")) {
    what = `${n} ${args[0]}`;
  } else if (n === "systemctl") {
    const { sub } = firstPositional(args, SYSTEMCTL_WITH_VALUE);
    if (SYSTEMCTL_POWER.has(sub)) what = `systemctl ${sub}`;
  }
  if (what === undefined) return undefined;
  return { rule: "destructive.shutdown", reason: `Shuts down or reboots the machine (${what})`, categories: DESTRUCTIVE };
}

/**
 * Cloud instance metadata endpoints (AWS, Azure, GCP, Alibaba, Oracle, ECS task credentials), also the
 * decimal and hex forms of 169.254.169.254. They hand out instance credentials to anything that asks.
 */
const METADATA_RE =
  /(^|[^0-9a-z.-])(169\.254\.169\.254|169\.254\.170\.2|100\.100\.100\.200|192\.0\.0\.192|fd00:ec2::254|2852039166|0xa9fea9fe|metadata\.google\.internal|metadata\.goog)($|[^0-9a-z-])|:\/\/metadata($|[/:?#])/i;
const METADATA_TOOLS = new Set([...NETWORK_TOOLS, "git"]);
const DEV_NET_RE = /\/dev\/(tcp|udp)\//;
const METADATA_HIT: Hit = {
  rule: "exfiltration.cloud_metadata",
  reason: "Contacts a cloud metadata endpoint, which hands out instance credentials",
  categories: ["secret_exfiltration", "network_egress"],
};

/** A network tool or interpreter names a metadata endpoint, or a /dev/tcp redirection opens one. */
function checkMetadata(c: Cmd): Hit | undefined {
  const args = c.argv.slice(1);
  if ((METADATA_TOOLS.has(c.name) || isInterpreter(c.name)) && args.some((a) => METADATA_RE.test(a))) return METADATA_HIT;
  const dev = (t: string) => DEV_NET_RE.test(t) && METADATA_RE.test(t);
  if (c.redirects.some((r) => dev(r.target.text)) || c.argv.some(dev)) return METADATA_HIT;
  return undefined;
}

function metadataHostIn(hosts: string[] | undefined): boolean {
  return !!hosts?.some((h) => METADATA_RE.test(h));
}

const DB_CLIENTS = new Set(["psql", "mysql", "mariadb", "sqlite3", "sqlcmd", "mongo", "mongosh", "redis-cli", "clickhouse-client", "clickhouse", "cockroach", "cqlsh", "duckdb", "snowsql", "bq", "pgcli", "mycli", "litecli", "usql"]);
const DESTRUCTIVE_SQL = /\b(drop\s+(database|schema|table|keyspace|collection)|truncate\s+(table\s+)?[\w."`]+|delete\s+from\s+[\w."`]+\s*(;|$)|flushall|flushdb|dropdatabase\s*\(|\.drop\s*\(\s*\))/i;

function checkDatabase(c: Cmd): Hit | undefined {
  const n = c.name;
  const args = c.argv.slice(1);
  if (n === "dropdb" || n === "dropuser") return { rule: "destructive.db_drop", reason: `Drops a database object (${n})`, categories: DESTRUCTIVE };
  if (DB_CLIENTS.has(n)) {
    const texts = [...args, ...c.redirects.map((r) => r.body ?? (r.op === "<<<" ? r.target.text : ""))];
    // `echo 'DROP TABLE x' | psql`: the SQL comes from another stage of the pipeline.
    for (const other of c.pipeline) if (other !== c) texts.push(...other.argv.slice(1));
    if (texts.some((t) => DESTRUCTIVE_SQL.test(t))) {
      return { rule: "destructive.db_drop", reason: "Drops, truncates or wipes database data", categories: DESTRUCTIVE };
    }
    if (n === "redis-cli" && args.some((a) => /^(flushall|flushdb)$/i.test(a))) {
      return { rule: "destructive.db_drop", reason: "Wipes a Redis database", categories: DESTRUCTIVE };
    }
  }
  const joined = args.join(" ");
  if (/(^|\s|:)db:(drop|reset|purge)(\s|$)/.test(` ${joined}`) || /\bdb:schema:load\b/.test(joined)) {
    return { rule: "destructive.db_drop", reason: "Drops or resets a database through a framework task", categories: DESTRUCTIVE };
  }
  if (/\bmigrate\s+reset\b/.test(joined) || /\b(db\s+push|migrate\s+\w+)\b.*--force-reset/.test(joined) || /--force-reset/.test(joined)) {
    return { rule: "destructive.db_drop", reason: "Resets a database and its data", categories: DESTRUCTIVE };
  }
  if (/^python[0-9.]*$/.test(n) && /\bmanage\.py\s+(flush|reset_db|sqlflush)\b/.test(joined)) {
    return { rule: "destructive.db_drop", reason: "Flushes a Django database", categories: DESTRUCTIVE };
  }
  return undefined;
}

const READ_ONLY_NAMES = new Set([
  "ls", "ll", "la", "cat", "bat", "head", "tail", "wc", "grep", "egrep", "fgrep", "rg", "ag", "ack", "tree", "pwd", "echo",
  "printf", "which", "whereis", "type", "file", "stat", "du", "df", "date", "whoami", "id", "uname", "hostname", "basename",
  "dirname", "realpath", "readlink", "sort", "uniq", "cut", "tr", "diff", "cmp", "comm", "jq", "nl", "xxd", "hexdump", "od",
  "strings", "md5sum", "sha1sum", "sha256sum", "sha512sum", "shasum", "md5", "cksum", "column", "seq", "sleep", "cd",
  "pushd", "popd", "dirs", "exa", "eza", "lsof", "ps", "pgrep", "uptime", "free", "nproc", "arch", "sw_vers", "locale",
  "test", "[", "[[", "true", "false", ":", "less", "more", "fold", "paste", "rev", "tac", "expand", "unexpand", "fmt",
  "numfmt", "join", "look", "units", "cal", "groups", "tty", "getconf", "vm_stat", "sysctl", "lscpu", "lsblk", "printenv",
]);

const DEPLOY_TOOLS = new Set(["vercel", "netlify", "flyctl", "fly", "heroku", "kamal", "cap", "capistrano", "eb", "serverless", "sls", "firebase", "amplify", "wrangler", "railway", "render", "dokku", "ansible-playbook"]);
const DEPLOY_VERB = /(deploy|release|rollout|promote|migrat|apply|upgrade|publish|ship)/i;

function checkDeploy(c: Cmd): Hit | undefined {
  if (READ_ONLY_NAMES.has(c.name) || c.name === "git" || c.name === "") return undefined;
  const tokens = [...c.argv, ...c.assigns];
  const verb = DEPLOY_TOOLS.has(c.name) || tokens.some((t) => DEPLOY_VERB.test(t) && t !== "--release");
  if (!verb) return undefined;
  const prod = tokens.find((t) => PROD_RE.test(t));
  if (!prod) return undefined;
  return { rule: "production.deploy", reason: `Looks like a production deploy or migration (${prod})`, categories: ["production_change"] };
}

// ---------------------------------------------------------------------------------------------------
// Script-level checks
// ---------------------------------------------------------------------------------------------------

function checkPipeToShell(cmds: Cmd[]): Hit | undefined {
  for (const c of cmds) {
    if (!isInterpreter(c.name) || c.stage === 0) continue;
    const before = c.pipeline.slice(0, c.stage);
    if (before.some((b) => FETCHERS.has(b.name)) && readsProgramFromStdin(c)) {
      return { rule: "rce.pipe_to_shell", reason: `Pipes a download straight into ${c.name}`, categories: ["remote_code_execution"] };
    }
  }
  return undefined;
}

function checkSubstitutionExec(cmds: Cmd[]): Hit | undefined {
  for (const c of cmds) {
    const execs = isInterpreter(c.name) || c.name === "source" || c.name === "." || c.name === "eval";
    if (!execs) continue;
    const scripts = c.words.flatMap((x) => [...x.subs, ...x.procSubs]);
    if (scripts.length && containsFetcher(scripts, c.cwd)) {
      return { rule: "rce.substitution", reason: `Runs downloaded code through ${c.name}`, categories: ["remote_code_execution"] };
    }
  }
  return undefined;
}

function checkDownloadThenRun(cmds: Cmd[]): Hit | undefined {
  const downloaded = new Set<string>();
  for (const c of cmds) {
    if (c.name === "curl" || c.name === "wget") {
      const args = c.argv.slice(1);
      for (let i = 0; i < args.length; i++) {
        const a = args[i]!;
        if ((c.name === "curl" && (a === "-o" || a === "--output")) || (c.name === "wget" && (a === "-O" || a === "--output-document"))) {
          if (args[i + 1] && args[i + 1] !== "-") downloaded.add(base(args[i + 1]!));
        } else if (a.startsWith("--output=") || a.startsWith("--output-document=")) downloaded.add(base(a.slice(a.indexOf("=") + 1)));
      }
      const url = args.find((a) => /^https?:\/\//.test(a));
      if (url && (c.name === "wget" ? !hasFlag(args, "-O", "--output-document", "-q-") : hasFlag(args, "-O", "--remote-name"))) {
        const b = base(url.replace(/[?#].*$/, ""));
        if (b) downloaded.add(b);
      }
      for (const r of redirectWrites(c)) downloaded.add(base(r.target.text));
      continue;
    }
    if (!downloaded.size) continue;
    const runsFile = (f: string | undefined) => f !== undefined && downloaded.has(base(f));
    if (c.argv[0] && c.argv[0].includes("/") && runsFile(c.argv[0])) {
      return { rule: "rce.download_run", reason: `Runs the downloaded file ${c.argv[0]}`, categories: ["remote_code_execution"] };
    }
    if (isInterpreter(c.name) || c.name === "source" || c.name === ".") {
      const script = c.argv.slice(1).find((a) => !a.startsWith("-"));
      if (runsFile(script)) return { rule: "rce.download_run", reason: `Runs the downloaded file ${script}`, categories: ["remote_code_execution"] };
    }
  }
  return undefined;
}

function isSecretSource(c: Cmd): boolean {
  const n = c.name;
  if ((n === "env" || n === "printenv" || n === "set") && c.argv.length <= 1) return true;
  if (n === "export" && (c.argv.length === 1 || c.argv[1] === "-p")) return true;
  if (n === "security" && /^(dump-keychain|find-generic-password|find-internet-password|export|find-certificate)$/.test(c.argv[1] ?? "")) return true;
  if (n === "gh" && c.argv[1] === "auth" && c.argv[2] === "token") return true;
  if (n === "gcloud" && c.argv.includes("print-access-token")) return true;
  if (n === "aws" && c.argv[1] === "configure" && ["get", "export-credentials"].includes(c.argv[2] ?? "")) return true;
  if (["ssh-add", "ssh-keygen", "ssh-copy-id"].includes(n)) return false;
  const args = c.argv.slice(1).filter((a, i, all) => !(["ssh", "scp", "sftp"].includes(n) && (all[i - 1] === "-i" || /^IdentityFile=/i.test(a))));
  if (args.some(mentionsSecret)) return true;
  if (c.redirects.some((r) => (r.op === "<" || r.op === "<<<") && mentionsSecret(r.target.text))) return true;
  return false;
}

function isNetworkCmd(c: Cmd): boolean {
  if (NETWORK_TOOLS.has(c.name)) return true;
  if (c.redirects.some((r) => /^\/dev\/(tcp|udp)\//.test(r.target.text))) return true;
  if (c.argv.some((a) => /\/dev\/(tcp|udp)\//.test(a))) return true;
  if ((/^python[0-9.]*$/.test(c.name) || c.name === "node" || c.name === "ruby" || c.name === "perl") && c.argv.some((a) => /\b(urllib|requests|http\.client|socket|fetch\(|https?\.request|Net::HTTP|LWP)\b/.test(a))) return true;
  return false;
}

function checkExfiltration(cmds: Cmd[], facts: RulesFacts): Hit | undefined {
  const source = cmds.find(isSecretSource);
  if (!source) return undefined;
  const sink = cmds.find(isNetworkCmd);
  if (sink || facts.network_hosts?.length) {
    return {
      rule: "exfiltration.secrets",
      reason: `Reads credentials (${source.name || "secret file"}) and contacts the network${sink ? ` (${sink.name})` : ""} in the same command`,
      categories: ["secret_exfiltration"],
    };
  }
  return undefined;
}

// ---------------------------------------------------------------------------------------------------
// Fast-allow
// ---------------------------------------------------------------------------------------------------

const DANGEROUS_ENV = /^(LD_[A-Z_]+|DYLD_[A-Z_]+|PATH|BASH_ENV|ENV|PROMPT_COMMAND|GIT_SSH_COMMAND|GIT_SSH|GIT_EXTERNAL_DIFF|GIT_PAGER|PAGER|EDITOR|VISUAL|GIT_EDITOR|NODE_OPTIONS|PYTHONPATH|PYTHONSTARTUP|PERL5OPT|PERL5LIB|RUBYOPT|GIT_CONFIG[A-Z_]*|GIT_DIR|GIT_WORK_TREE|SHELLOPTS|IFS|GIT_ASKPASS|SSH_ASKPASS|LESSOPEN|MANPAGER)\+?=/;

const SAFE_SCRIPT = /^(test|tests|lint|build|typecheck|type-check|types|check|tsc|format:check|fmt:check|prettier:check|vet|compile|coverage|spec|e2e|unit)([:._-][\w:.-]*)?$/i;
const UNSAFE_SCRIPT = /(deploy|publish|release|prod|push|upload|install|clean|reset|drop|migrat|seed|start|serve|dev|watch)/i;

function safeScriptName(s: string | undefined): boolean {
  return !!s && SAFE_SCRIPT.test(s) && !UNSAFE_SCRIPT.test(s);
}

const TEST_TOOLS: Record<string, (args: string[]) => boolean> = {
  tsc: (a) => !hasFlag(a, "--watch", "-w"),
  vitest: (a) => !["dev", "watch", "init"].includes(a[0] ?? ""),
  jest: () => true,
  mocha: () => true,
  ava: () => true,
  eslint: () => true,
  prettier: (a) => hasFlag(a, "--check", "-c", "--list-different", "-l"),
  biome: (a) => ["check", "lint", "ci", "format"].includes(a[0] ?? "") && !hasFlag(a, "--write", "--apply", "--fix", "--unsafe"),
  stylelint: () => true,
  pytest: () => true,
  "py.test": () => true,
  mypy: () => true,
  pyright: () => true,
  ruff: (a) => a[0] === "check" || (a[0] === "format" && hasFlag(a, "--check", "--diff")),
  black: (a) => hasFlag(a, "--check", "--diff"),
  isort: (a) => hasFlag(a, "--check", "--check-only", "-c", "--diff"),
  flake8: () => true,
  pylint: () => true,
  unittest: () => true,
  rspec: () => true,
  rubocop: (a) => !hasFlag(a, "-a", "-A", "--autocorrect", "--auto-correct", "--autocorrect-all"),
  shellcheck: () => true,
  staticcheck: () => true,
  "golangci-lint": (a) => a[0] === "run",
  gofmt: (a) => !hasFlag(a, "-w"),
  playwright: (a) => a[0] === "test",
  cypress: (a) => a[0] === "run",
  "vue-tsc": (a) => !hasFlag(a, "--watch", "-w"),
  "svelte-check": () => true,
  ctest: () => true,
};

function outputInside(c: Cmd, ctx: Ctx, flags: string[]): boolean {
  const args = c.argv.slice(1);
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!;
    for (const f of flags) {
      let v: string | undefined;
      if (a === f) v = args[i + 1];
      else if (a.startsWith(`${f}=`)) v = a.slice(f.length + 1);
      if (v === undefined) continue;
      const p = resolvePath(v, c.cwd);
      if (!p || (!isInside(p, ctx.roots) && p !== "/dev/null")) return false;
    }
  }
  return true;
}

function cwdInside(c: Cmd, ctx: Ctx): boolean {
  return c.cwd !== undefined && isInside(c.cwd, ctx.roots);
}

function isSafeRunner(c: Cmd, ctx: Ctx): boolean {
  const n = c.name;
  const args = c.argv.slice(1);
  const PM_OPTS = new Set(["--filter", "-F", "-C", "--dir", "--prefix", "--workspace", "-w"]);
  if (n === "npm" || n === "pnpm" || n === "yarn" || n === "bun") {
    if (!outputInside(c, ctx, ["-C", "--dir", "--prefix"])) return false;
    let i = 0;
    while (i < args.length && args[i]!.startsWith("-")) i += PM_OPTS.has(args[i]!) ? 2 : 1;
    const sub = args[i];
    const rest = args.slice(i + 1);
    if (sub === undefined) return false;
    if (["test", "t", "tst", "lint", "typecheck", "build", "check"].includes(sub)) return cwdInside(c, ctx);
    if (["ls", "list", "ll", "la", "outdated", "why", "explain", "audit", "view", "info", "config"].includes(sub)) return sub !== "config" || rest[0] === "get" || rest[0] === "list";
    if (sub === "run" || sub === "run-script") {
      const name = rest.find((x) => !x.startsWith("-"));
      return safeScriptName(name) && cwdInside(c, ctx);
    }
    if (sub === "exec" || sub === "x") return isSafeTool(rest, c, ctx);
    if (n === "yarn" || n === "bun" || n === "pnpm") return safeScriptName(sub) && cwdInside(c, ctx);
    return false;
  }
  if (n === "npx" || n === "pnpx" || n === "bunx") {
    const rest = args.filter((a, i) => !(a.startsWith("-") && i === 0 && a !== "--"));
    return isSafeTool(rest[0] === "--" ? rest.slice(1) : rest, c, ctx);
  }
  if (n === "uv" || n === "poetry" || n === "pipenv" || n === "pdm" || n === "hatch") {
    const { sub, rest } = firstPositional(args, new Set(["--directory", "-p", "--python", "--with", "--project"]));
    if (sub === "run") return isSafeTool(rest.filter((a) => !a.startsWith("--") || a === "--"), c, ctx);
    if (sub === "pip") return ["list", "show", "freeze", "check"].includes(rest[0] ?? "");
    return false;
  }
  if (n === "bundle" && args[0] === "exec") return isSafeTool(args.slice(1), c, ctx);
  return isSafeTool([n, ...args], c, ctx);
}

/** Test runners, linters and build tools (argv[0] is the tool). */
function isSafeTool(argv: string[], c: Cmd, ctx: Ctx): boolean {
  const tool = base(argv[0] ?? "");
  const args = argv.slice(1);
  if (!tool) return false;
  if (/^python[0-9.]*$/.test(tool)) {
    const mi = args.indexOf("-m");
    if (mi >= 0 && args[mi + 1]) return isSafeTool([args[mi + 1]!, ...args.slice(mi + 2)], c, ctx);
    if (args.length === 1 && ["--version", "-V"].includes(args[0]!)) return true;
    return false;
  }
  // Own properties only: a plain-object lookup would also find Object.prototype members
  // (`toString`, `constructor`, `__proto__`, ...) and fast-allow programs with those names.
  const testTool = Object.hasOwn(TEST_TOOLS, tool) ? TEST_TOOLS[tool] : undefined;
  if (testTool) {
    if (tool === "tsc" || tool === "vue-tsc") return testTool(args) && outputInside(c, ctx, ["--outDir", "--outFile", "--declarationDir"]);
    return testTool(args);
  }
  if (tool === "go") {
    const sub = args[0];
    if (hasFlag(args, "-exec", "-toolexec", "--exec")) return false;
    if (["test", "vet", "build", "list", "version", "doc"].includes(sub ?? "")) {
      return outputInside({ ...c, argv }, ctx, ["-o"]) && (sub === "build" || sub === "test" ? cwdInside(c, ctx) : true);
    }
    if (sub === "env") return !hasFlag(args, "-w", "-u");
    if (sub === "mod") return ["graph", "why", "verify"].includes(args[1] ?? "");
    return false;
  }
  if (tool === "cargo") {
    const sub = args.find((a) => !a.startsWith("-") && !a.startsWith("+"));
    if (["test", "build", "check", "doc", "tree", "metadata", "bench", "nextest"].includes(sub ?? "")) return cwdInside(c, ctx) && outputInside({ ...c, argv }, ctx, ["--target-dir", "--out-dir", "--artifact-dir"]);
    if (sub === "clippy") return !hasFlag(args, "--fix");
    if (sub === "fmt") return hasFlag(args, "--check");
    return false;
  }
  if (tool === "make" || tool === "gmake") {
    if (!outputInside({ ...c, argv }, ctx, ["-C", "--directory"])) return false;
    const targets: string[] = [];
    for (let i = 0; i < args.length; i++) {
      const a = args[i]!;
      if (["-j", "-l", "-f", "-C", "--directory", "--file", "-o", "-W"].includes(a)) {
        if (a === "-f" || a === "--file" || a === "-o" || a === "-W") return false;
        i++;
        continue;
      }
      if (a.startsWith("-") || ASSIGN_RE.test(a)) continue;
      targets.push(a);
    }
    return cwdInside(c, ctx) && targets.length > 0 && targets.every((t) => safeScriptName(t) || t === "all");
  }
  if (["mvn", "mvnw", "gradle", "gradlew"].includes(tool)) {
    const tasks = args.filter((a) => !a.startsWith("-"));
    return cwdInside(c, ctx) && tasks.length > 0 && tasks.every((t) => /^([\w-]*:)*(test|verify|compile|build|check|lint|assemble|package)$/.test(t));
  }
  if (tool === "dotnet" || tool === "swift") return ["test", "build"].includes(args[0] ?? "") && cwdInside(c, ctx);
  if (tool === "bazel" || tool === "bazelisk") return ["test", "build", "query", "info"].includes(args[0] ?? "") && cwdInside(c, ctx);
  if (tool === "turbo" || tool === "nx") {
    const t = args[0] === "run" ? args.slice(1) : args;
    const names = t.filter((a) => !a.startsWith("-"));
    return names.length > 0 && names.every((x) => safeScriptName(x.split(":").pop()));
  }
  if (tool === "rake") return args.length > 0 && args.every((a) => a.startsWith("-") || ["test", "spec", "lint"].includes(a));
  if (tool === "pip" || tool === "pip3") return ["list", "show", "freeze", "check"].includes(args[0] ?? "");
  return false;
}

const GIT_READ = new Set([
  "status", "diff", "log", "show", "blame", "rev-parse", "describe", "ls-files", "ls-tree", "grep", "shortlog", "cat-file",
  "merge-base", "name-rev", "rev-list", "for-each-ref", "whatchanged", "count-objects", "check-ignore", "show-ref", "help", "version",
  "var", "range-diff", "annotate", "cherry", "verify-commit", "verify-tag", "difftool",
]);

function isSafeGit(c: Cmd): boolean {
  const { sub, args, globals } = gitSub(c.argv);
  if (globals.some((g) => g === "-c" || g === "--config-env" || g.startsWith("--exec-path") || g === "--exec-path")) return false;
  if (hasFlag(args, "--output", "--ext-diff", "--exec", "--upload-pack")) return false;
  if (sub === "difftool") return false;
  if (GIT_READ.has(sub)) return true;
  const p = parseArgs(args);
  switch (sub) {
    case "branch":
      return [...p.shorts].every((s) => ["a", "r", "v", "l", "vv"].includes(s)) && [...p.longs.keys()].every((k) => ["--list", "--all", "--remotes", "--show-current", "--contains", "--merged", "--no-merged", "--sort", "--format", "--verbose", "--color", "--no-color"].includes(k)) && (p.positionals.length === 0 || p.longs.has("--list") || p.shorts.has("l") || p.longs.has("--contains") || p.longs.has("--merged") || p.longs.has("--no-merged"));
    case "tag":
      return (args.length === 0 || p.shorts.has("l") || p.longs.has("--list")) && !p.shorts.has("d") && !p.shorts.has("a") && !p.shorts.has("s") && !p.shorts.has("f");
    case "remote":
      return args.length === 0 || ["-v", "--verbose", "show", "get-url"].includes(args[0]!);
    case "stash":
      return ["list", "show"].includes(args[0] ?? "");
    case "config":
      return hasFlag(args, "--get", "--get-all", "--list", "-l", "--get-regexp") || ["get", "list"].includes(args[0] ?? "");
    case "reflog":
      return args.length === 0 || args[0] === "show" || args[0]!.startsWith("-");
    case "worktree":
      return args[0] === "list";
    case "submodule":
      return args.length === 0 || args[0] === "status";
    default:
      return false;
  }
}

function isSafeReadOnly(c: Cmd): boolean {
  const n = c.name;
  const args = c.argv.slice(1);
  if (n === "printenv") return args.length > 0; // a single variable; a full dump may hold secrets
  if (!READ_ONLY_NAMES.has(n)) return false;
  if (["cat", "bat", "head", "tail", "less", "more", "grep", "rg", "egrep", "fgrep", "ag", "xxd", "strings", "nl", "od", "hexdump"].includes(n) && args.some(mentionsSecret)) return false;
  if (c.redirects.some((r) => (r.op === "<" || r.op === "<<<") && mentionsSecret(r.target.text))) return false;
  if (n === "sort" && hasFlag(args, "-o", "--output")) return false;
  if ((n === "uniq" || n === "xxd") && parseArgs(args).positionals.length > 1) return false;
  if (n === "sysctl" && args.some((a) => a.includes("=") || a === "-w")) return false;
  if (n === "rg" && hasFlag(args, "--pre")) return false;
  if ((n === "less" || n === "more") && c.stage === 0 && args.length === 0) return false;
  if (n === "date" && hasFlag(args, "-s", "--set")) return false;
  if (n === "hostname" && parseArgs(args).positionals.length > 0) return false;
  return true;
}

function isSafeCmd(c: Cmd, ctx: Ctx, extraAllow: RegExp[]): boolean {
  if (c.escalated) return false;
  if (c.assigns.some((a) => DANGEROUS_ENV.test(a))) return false;
  if (extraAllow.some((re) => re.test(c.text))) return true;
  if (c.unknownArgs && !["echo", "grep", "rg", "wc", "cat", "head", "ls"].includes(c.name)) return false;
  for (const r of redirectWrites(c)) {
    const t = resolvePath(r.target.text, c.cwd);
    if (t === "/dev/null" || t === "/dev/stdout" || t === "/dev/stderr") continue;
    if (!t || !isInside(t, ctx.roots) || /(^|\/)\.git(\/|$)/.test(t) || isSecretPath(t)) return false;
  }
  const n = c.name;
  const args = c.argv.slice(1);
  if (n === "" || NEUTRAL.has(n)) return true;
  if (n === "command" && (args[0] === "-v" || args[0] === "-V")) return true;
  if (args.length === 1 && ["--version", "-V", "--help", "version"].includes(args[0]!) && !n.includes("/")) return true;
  if (isSafeReadOnly(c)) return true;
  // find -exec / fd -x: the executed command is checked as its own simple command.
  if (n === "find") return !args.some((a) => ["-delete", "-fprint", "-fprint0", "-fprintf", "-fls"].includes(a));
  if (n === "fd" || n === "fdfind") return true;
  if (SHELLS.has(n) && args.some((a) => /^-[a-zA-Z]*c[a-zA-Z]*$/.test(a))) return true; // body checked separately
  if (n === "sed" || n === "gsed") {
    if (args.some((a) => /^-[a-zA-Z]*i/.test(a) || a.startsWith("--in-place") || a === "-I")) return false;
    return !args.some((a) => /(^|[;{}\n]\s*)[0-9,$/!]*\s*[wWe](\s|$)|\/[gIipm0-9]*[wWe](\s|$)/.test(a));
  }
  if (n === "awk" || n === "gawk" || n === "mawk") {
    if (hasFlag(args, "-f", "-i", "--include")) return false;
    return !args.some((a) => /system\s*\(|\||>|getline|close\s*\(|fflush|inplace/.test(a));
  }
  if (n === "yq") return !hasFlag(args, "-i", "--inplace");
  if (n === "tee") {
    return parseArgs(args).positionals.every((t) => {
      const p = resolvePath(t, c.cwd);
      return p !== undefined && (p === "/dev/null" || (isInside(p, ctx.roots) && !/(^|\/)\.git(\/|$)/.test(p)));
    });
  }
  if (n === "crontab") return args.length === 1 && args[0] === "-l";
  if (n === "git") return isSafeGit(c);
  if (n === "kubectl" || n === "oc") {
    const { sub, rest } = firstPositional(args, KUBECTL_WITH_VALUE);
    if (["get", "describe"].includes(sub)) return !/^secrets?$|^secrets?\//.test(rest.find((x) => !x.startsWith("-")) ?? "");
    return ["logs", "top", "explain", "version", "api-resources", "api-versions", "cluster-info"].includes(sub) || (sub === "config" && ["get-contexts", "current-context", "view"].includes(rest[0] ?? "") && !hasFlag(rest, "--raw"));
  }
  if (n === "docker" || n === "podman") {
    if (["ps", "images", "logs", "inspect", "version", "info", "stats"].includes(args[0] ?? "")) return args[0] !== "stats" || hasFlag(args, "--no-stream");
    return args[0] === "compose" && ["ps", "logs", "config", "ls", "images"].includes(args[1] ?? "");
  }
  if (n === "terraform" || n === "tofu") return ["validate", "version", "providers"].includes(args[0] ?? "") || (args[0] === "fmt" && hasFlag(args, "-check"));
  if (n === "gh") {
    const [g, s] = args;
    if (g === "api") return !hasFlag(args, "-X", "--method", "-f", "-F", "--field", "--raw-field", "--input");
    return ["pr", "issue", "run", "repo", "release", "workflow"].includes(g ?? "") && ["view", "list", "diff", "checks", "status"].includes(s ?? "");
  }
  if (n === "brew") return ["list", "info", "search", "outdated", "config", "doctor", "deps", "leaves"].includes(args[0] ?? "");
  return isSafeRunner(c, ctx);
}

// ---------------------------------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------------------------------

const READ_TOOLS = new Set(["Read", "Glob", "Grep", "LS", "NotebookRead", "TodoWrite", "TaskStop", "TerminalList", "TerminalRead", "TerminalOpen", "TerminalStop", "TaskOutput", "BashOutput", "KillShell", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "WebSearch", "Task", "Agent"]);
const WRITE_TOOLS = new Set(["Write", "Edit", "MultiEdit", "NotebookEdit"]);

function ask(reason: string, rule = "ask.default"): RulesResult {
  return { decision: "ask", reason, categories: [], rule };
}

function sortCategories(cats: Iterable<RuleCategory>): RuleCategory[] {
  const set = new Set(cats);
  return RULE_CATEGORIES.filter((c) => set.has(c));
}

function denyFrom(hits: Hit[]): RulesResult {
  const first = hits[0]!;
  return { decision: "deny", reason: first.reason, categories: sortCategories(hits.flatMap((h) => h.categories)), rule: first.rule };
}

function compile(patterns: string[] | undefined): RegExp[] {
  const out: RegExp[] = [];
  for (const p of patterns ?? []) {
    try {
      out.push(new RegExp(p));
    } catch {
      // invalid admin regex: ignored here (the admin API validates on save)
    }
  }
  return out;
}

function parseInput(input: RulesInput["input"]): Record<string, unknown> | undefined {
  if (typeof input !== "string") return input && typeof input === "object" ? input : undefined;
  if (!input.trim()) return {};
  try {
    const v = JSON.parse(input) as unknown;
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;
  } catch {
    return undefined;
  }
}

function normRoot(p: string): string | undefined {
  if (!p) return undefined;
  if (p.startsWith("/") || p === "~" || p.startsWith("~/")) return normalizePath(p);
  return undefined;
}

export function evaluateRules(req: RulesInput, opts: RulesOptions = {}): RulesResult {
  const input = parseInput(req.input);
  if (!input) return ask("Tool input is not a JSON object", "ask.bad_input");
  const facts = req.facts ?? {};
  const cwd = normRoot(req.cwd ?? "");
  const roots = [cwd, ...(req.additional_directories ?? []).map(normRoot)].filter((x): x is string => !!x);
  const ctx: Ctx = {
    roots,
    cwd,
    facts,
    protectedBranches: (opts.protectedBranches?.length ? opts.protectedBranches : DEFAULT_PROTECTED_BRANCHES).map(globToRegExp),
    denyAllForcePush: !!opts.denyAllForcePush,
  };
  const extraAllow = compile(opts.extraAllow);
  const extraDeny = (opts.extraDeny ?? []).flatMap((r) => compile([r.pattern]).map((re) => ({ re, r })));
  const tool = req.tool_name;

  if (tool !== "Bash") {
    const subject = `${tool} ${typeof req.input === "string" ? req.input : JSON.stringify(req.input)}`;
    const extra = extraDeny.find(({ re }) => re.test(subject));
    if (extra) return { decision: "deny", reason: extra.r.reason || "Blocked by an administrator rule", categories: sortCategories(extra.r.categories ?? []), rule: "admin.deny" };
    if ((tool === "WebFetch" && typeof input.url === "string" && METADATA_RE.test(input.url)) || metadataHostIn(facts.network_hosts)) return denyFrom([METADATA_HIT]);
    return evaluateTool(tool, input, ctx, facts, extraAllow.some((re) => re.test(subject)));
  }

  const command = typeof input.command === "string" ? input.command : undefined;
  if (command === undefined) return ask("Bash call without a command", "ask.bad_input");
  if (command.length > MAX_COMMAND_LENGTH) return ask("Command is too long to analyze", "ask.too_long");

  const hits: Hit[] = [];
  for (const { re, r } of extraDeny) {
    if (re.test(command)) hits.push({ rule: "admin.deny", reason: r.reason || "Blocked by an administrator rule", categories: r.categories ?? [] });
  }

  const sources = [command, ...(facts.subcommands ?? []).filter((s) => s && s !== command)];
  let allCmds: Cmd[] = [];
  let parsedMain = true;
  for (const [idx, src] of sources.entries()) {
    const cmds: Cmd[] = [];
    try {
      flatten(parseShell(src), cwd, cmds);
    } catch {
      if (idx === 0) {
        parsedMain = false;
        hits.push(...rawFallback(src));
      }
      continue;
    }
    for (const c of cmds) {
      for (const check of [checkEscalation, checkRm, checkFindDelete, checkDevices, checkPrivilegedWrites, checkChmod, checkGit, checkInfra, checkDeploy, checkShutdown, checkCloudDelete, checkMetadata] as ((c: Cmd, ctx: Ctx) => Hit | undefined)[]) {
        const h = check(c, ctx);
        if (h) hits.push(h);
      }
      const db = checkDatabase(c);
      if (db) hits.push(db);
      for (const { re, r } of extraDeny) {
        if (re.test(c.text)) hits.push({ rule: "admin.deny", reason: r.reason || "Blocked by an administrator rule", categories: r.categories ?? [] });
      }
    }
    for (const h of [checkPipeToShell(cmds), checkSubstitutionExec(cmds), checkDownloadThenRun(cmds)]) if (h) hits.push(h);
    if (idx === 0) allCmds = cmds;
    else allCmds.push(...cmds);
  }
  const exfil = checkExfiltration(allCmds, facts);
  if (exfil) hits.push(exfil);
  if (metadataHostIn(facts.network_hosts)) hits.push(METADATA_HIT);
  // Paths the client resolved for the whole call.
  for (const p of facts.write_paths ?? []) {
    const r = resolvePath(p, undefined);
    if (!r) continue;
    if (isBlockDevice(r)) hits.push({ rule: "destructive.device_write", reason: `Writes directly to the block device ${r}`, categories: DESTRUCTIVE });
    else if (isPrivilegedWritePath(r)) hits.push({ rule: "privilege.protected_write", reason: `Modifies ${r} (shell startup, ssh, scheduler or system file)`, categories: ["privilege_escalation"] });
  }
  if (hits.length) return denyFrom(dedupe(hits));
  if (!parsedMain) return ask("Could not parse the command", "ask.unparsed");
  if (facts.network_hosts?.length) return ask("Contacts the network", "ask.network");
  if (allCmds.length && allCmds.every((c) => isSafeCmd(c, ctx, extraAllow))) {
    return { decision: "allow", reason: "Read-only command or a test, lint or build run inside the workspace", categories: [], rule: "allow.safe_command" };
  }
  return ask("Not covered by the built-in rules", "ask.default");
}

function dedupe(hits: Hit[]): Hit[] {
  const seen = new Set<string>();
  return hits.filter((h) => {
    const k = `${h.rule}\0${h.reason}`;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

/** Last-resort patterns for commands the parser cannot read. */
function rawFallback(src: string): Hit[] {
  const out: Hit[] = [];
  if (/\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|da|k)?sh\b/.test(src)) {
    out.push({ rule: "rce.pipe_to_shell", reason: "Pipes a download straight into a shell", categories: ["remote_code_execution"] });
  }
  if (/\brm\s+(-[a-zA-Z]*[rR][a-zA-Z]*\s+)+(--\s+)?["']?(\/|~|\$HOME|\$\{HOME\})["']?(\s|$|\*)/.test(src)) {
    out.push({ rule: "destructive.rm_critical", reason: "Removes the root or home directory", categories: DESTRUCTIVE });
  }
  if (/(^|[;&|\s])sudo\s/.test(src)) out.push({ rule: "privilege.sudo", reason: "Runs a command with elevated privileges (sudo)", categories: ["privilege_escalation"] });
  return out;
}

function evaluateTool(tool: string, input: Record<string, unknown>, ctx: Ctx, facts: RulesFacts, adminAllowed: boolean): RulesResult {
  if (WRITE_TOOLS.has(tool)) {
    const raw = typeof input.file_path === "string" ? input.file_path : typeof input.notebook_path === "string" ? input.notebook_path : undefined;
    if (!raw) return ask("Write without a target path", "ask.bad_input");
    const p = resolvePath(raw, ctx.cwd);
    const paths = [p, ...(facts.write_paths ?? []).map((x) => resolvePath(x, undefined))];
    for (const x of paths) {
      if (x && isPrivilegedWritePath(x)) {
        return { decision: "deny", reason: `Modifies ${x} (shell startup, ssh, scheduler or system file)`, categories: ["privilege_escalation"], rule: "privilege.protected_write" };
      }
      if (x && isBlockDevice(x)) return { decision: "deny", reason: `Writes directly to the block device ${x}`, categories: DESTRUCTIVE, rule: "destructive.device_write" };
    }
    if (adminAllowed) return { decision: "allow", reason: "Allowed by an administrator rule", categories: [], rule: "admin.allow" };
    if (!p) return ask("Cannot resolve the target path", "ask.path");
    if (/(^|\/)\.git\/(hooks|config)(\/|$)/.test(p)) return ask("Modifies git hooks or configuration", "ask.git_internal");
    if (paths.every((x) => x !== undefined && isInside(x, ctx.roots))) {
      return { decision: "allow", reason: "Edits a file inside the working directories", categories: [], rule: "allow.workspace_write" };
    }
    return ask("Writes outside the working directories", "ask.outside_write");
  }
  if (READ_TOOLS.has(tool)) {
    const target = [input.file_path, input.path, input.notebook_path].find((x): x is string => typeof x === "string");
    if (target && (mentionsSecret(target) || (resolvePath(target, ctx.cwd) && isSecretPath(resolvePath(target, ctx.cwd)!)))) {
      return ask("Reads a credentials file", "ask.secret_read");
    }
    if (typeof input.pattern === "string" && tool === "Glob" && mentionsSecret(input.pattern)) return ask("Looks for credentials files", "ask.secret_read");
    return { decision: "allow", reason: "Read-only tool", categories: [], rule: "allow.read_tool" };
  }
  if (adminAllowed) return { decision: "allow", reason: "Allowed by an administrator rule", categories: [], rule: "admin.allow" };
  return ask("Not covered by the built-in rules", "ask.default");
}
