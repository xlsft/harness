// Demo data for the Files pane (docs/design/ui/Files.dc.html): the project tree with git marks,
// evals/run/client.go highlighted, the rendered baseline report, in-file search hits and the
// settings menu. The agent contract has no file browsing yet.

export type FileKind = 'dir' | 'go' | 'md' | 'file'

export interface FileNode {
  path: string
  kind: FileKind
  // Git mark: M modified, A added, ● changes inside the folder.
  git: string
}

const N = (path: string, kind: FileKind, git = ''): FileNode => ({ path, kind, git })

export const FILE_TREE: FileNode[] = [
  N('docs', 'dir'), N('docs/assets', 'dir'), N('docs/design', 'dir'), N('docs/evals', 'dir'), N('docs/evals/baseline-m1.md', 'md', 'A'),
  N('docs/protocol', 'dir'), N('docs/PLAN.md', 'md'),
  N('evals', 'dir', '●'), N('evals/results', 'dir'), N('evals/run', 'dir'),
  N('evals/run/client.go', 'go', 'M'), N('evals/run/main.go', 'go'), N('evals/run/proc_other.go', 'go'), N('evals/run/proc_unix.go', 'go'), N('evals/run/report.go', 'go'), N('evals/run/selftest.go', 'go', 'A'), N('evals/run/workspace.go', 'go'),
  N('evals/task', 'dir'), N('evals/tasks', 'dir'), N('evals/.gitignore', 'file'), N('evals/go.mod', 'file'), N('evals/go.sum', 'file'), N('evals/README.md', 'md'),
  N('packages', 'dir', '●'), N('proto', 'dir'), N('scripts', 'dir'), N('buf.gen.yaml', 'file'), N('LICENSE', 'file'), N('README.md', 'md')
]

// Folders expanded when the pane opens (the design's "code" state).
export const OPEN_DIRS = ['docs', 'docs/evals', 'evals', 'evals/run']

export const CODE_FILE = 'evals/run/client.go'
export const MD_FILE = 'docs/evals/baseline-m1.md'
export const CODE_LANG = 'Go · UTF-8 · LF'
// The current line (1-based).
export const CODE_CURRENT_LINE = 16

// Folder details shown when a folder is selected.
export const FOLDER_INFO: Record<string, string> = {
  'evals/results': '1 item · 48 KB'
}

export interface CodeToken {
  t: string
  c: string
}

const K = '#f6339a'
const S = '#00bc7d'
const C = '#8f8f8f'
const F = '#2b7fff'
const Ty = '#00b8db'
const D = '#d4d4d4'
const x = (t: string, c: string): CodeToken => ({ t, c })

export const CODE_LINES: CodeToken[][] = [
  [x('package', K), x(' main', D)], [],
  [x('import', K), x(' (', D)], [x('    "bytes"', S)], [x('    "encoding/json"', S)], [x('    "github.com/xlsft/harness/evals/task"', S)], [x('    "strconv"', S)], [x('    "strings"', S)], [x(')', D)], [],
  [x('// clientArgs builds the harness-client command line for one run.', C)],
  [x('//', C)],
  [x('// This is the single place that knows the headless CLI contract.', C)],
  [x('// Adjust it here if the flags of `harness-client` change.', C)],
  [x('func ', K), x('clientArgs', F), x('(cfg *', D), x('config', Ty), x(', model ', D), x('string', Ty), x(', t *', D), x('task.Task', Ty), x(') []', D), x('string', Ty), x(' {', D)],
  [x('    args := []', D), x('string', Ty), x('{', D)],
  [x('        "-p"', S), x(', t.Prompt,', D)],
  [x('        "--model"', S), x(', model,', D)],
  [x('        "--output-format"', S), x(', ', D), x('"json"', S), x(',', D)],
  [x('        "--max-turns"', S), x(', strconv.', D), x('Itoa', F), x('(t.MaxTurns),', D)],
  [x('        "--permission-mode"', S), x(', ', D), x('"bypassPermissions"', S), x(',', D)],
  [x('        "--no-session-persistence"', S), x(',', D)],
  [x('    }', D)],
  [x('    if', K), x(' cfg.server != ', D), x('""', S), x(' {', D)],
  [x('        args = ', D), x('append', F), x('(args, ', D), x('"--server"', S), x(', cfg.server)', D)],
  [x('    }', D)],
  [x('    return ', K), x('append', F), x('(args, cfg.extraArgs...)', D)],
  [x('}', D)]
]

export const BASELINE = {
  title: 'Eval baseline: M1 (2026-11-02)',
  intro: 'Client: M0 + M1 context packages. Server: M1 core (Anthropic, OpenAI-chat and Responses adapters). Models via OpenRouter. One attempt per task; total cost about $0.093.',
  results: [
    { model: 'claude-haiku', rate: '100%', passed: '7/7', color: '#00bc7d' },
    { model: 'gpt-luna', rate: '100%', passed: '7/7', color: '#00bc7d' },
    { model: 'deepseek-flash', rate: '100%', passed: '7/7', color: '#00bc7d' },
    { model: 'qwen-flash', rate: '100%', passed: '7/7', color: '#00bc7d' },
    { model: 'glm-flash', rate: '86%', passed: '6/7', color: '#fe9a00' }
  ],
  failures: [
    { model: 'glm-flash', task: 'py-implement-from-docstring', status: 'fail', turns: '7' }
  ]
}

// Markdown source behind the rendered report (the "Show source" toggle).
export const BASELINE_SOURCE = `# ${BASELINE.title}

${BASELINE.intro}

| Model | Pass rate | Passed | Timeouts |
|---|---:|---:|---:|
${BASELINE.results.map(r => `| ${r.model} | ${r.rate} | ${r.passed} | 0 |`).join('\n')}

## Failures

| Model | Task | Status | Turns |
|---|---|---|---:|
${BASELINE.failures.map(f => `| ${f.model} | ${f.task} | ${f.status} | ${f.turns} |`).join('\n')}
`

export const SEARCH_QUERY = '?--server'
export const SEARCH_SUMMARY = '5 results in 3 files'

export interface SearchHit {
  // A file header row, or a matching line under it.
  file: boolean
  line: string
  before: string
  match: string
  after: string
}

const hit = (file: boolean, line: string, before: string, match = '', after = ''): SearchHit => ({ file, line, before, match, after })

export const SEARCH_HITS: SearchHit[] = [
  hit(true, '', 'evals/run/client.go'),
  hit(false, '25', 'args = append(args, "', '--server', '", …'),
  hit(true, '', 'evals/run/main.go'),
  hit(false, '31', 'flag.StringVar(&cfg.server, "', '--server', '"'),
  hit(false, '58', 'if cfg.server == "" && ', '--server', '…'),
  hit(true, '', 'docs/cli-reference.md'),
  hit(false, '112', '| `', '--server', ' <url>` | harness server…')
]

export interface FilesMenuItem {
  key: string
  label: string
  value?: string
  // Checkable item (state lives in the pane); `sub` opens a submenu.
  check?: boolean
  sub?: boolean
}

export const FILES_MENU: (FilesMenuItem | 'sep')[] = [
  { key: 'tree', label: 'Show file tree', value: '⇧⌘Y', check: true },
  { key: 'treePos', label: 'File tree position', value: 'Automatic', sub: true },
  'sep',
  { key: 'previewTabs', label: 'Preview tabs', check: true },
  { key: 'autoSave', label: 'Auto save', check: true },
  { key: 'tabIndents', label: 'Tab key indents', check: true },
  'sep',
  { key: 'wrap', label: 'Word wrap', check: true },
  { key: 'guides', label: 'Indent guides', check: true },
  { key: 'whitespace', label: 'Show whitespace', check: true },
  { key: 'pastEnd', label: 'Scroll past end', check: true },
  { key: 'tabSize', label: 'Tab size', sub: true }
]

export const FILES_SETTINGS_DEFAULTS: Record<string, boolean> = {
  previewTabs: true,
  autoSave: true,
  tabIndents: true,
  wrap: true,
  guides: true,
  whitespace: false,
  pastEnd: false
}

export const TAB_SIZES = [2, 4, 8]
