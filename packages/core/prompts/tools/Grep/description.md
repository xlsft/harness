Searches file contents with a regular expression, using ripgrep. Prefer it to running `grep` or `rg` through Bash.

- `pattern` uses ripgrep (Rust regex) syntax. Escape regex characters you mean literally, for example `\{\{name\}\}` to find `{{name}}` or `\.then\(` to find `.then(`.
- `path`: absolute file or directory to search; defaults to the working directory.
- Narrow the files with `glob` (for example `*.tsx` or `**/*.{ts,js}`) or `type` (a ripgrep type such as `js`, `py`, `go`, `rust`).
- `output_mode`:
  - `files_with_matches` (default): only the paths of matching files;
  - `content`: the matching lines, with line numbers (`-n`, on by default) and optional context lines after (`-A`), before (`-B`) or around (`-C`) each match;
  - `count`: number of matches per file.
- `-i`: ignore case. `multiline`: let a pattern span lines, with `.` matching newlines (for example `func \w+\([\s\S]*?\) error`).
- `head_limit`: keep only the first N lines or entries.
- Files ignored by `.gitignore` are skipped.
- Start broad with `files_with_matches`, then use `content` on the files that matter.
