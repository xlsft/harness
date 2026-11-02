Searches file contents with regular expressions, built on ripgrep.

- `pattern` uses ripgrep (Rust regex) syntax. Literal braces and other special characters must be escaped, for example `interface\{\}` to find `interface{}` in Go code.
- `path` is a file or directory to search (absolute; defaults to the current working directory). Narrow the search with `glob` (for example `*.tsx` or `**/*.{ts,js}`) or `type` (ripgrep file type such as `js`, `py`, `rust`).
- `output_mode`:
  - `files_with_matches` (default): list of matching file paths;
  - `content`: matching lines; supports `-n` (line numbers, on by default), `-A` / `-B` / `-C` (context lines after / before / around);
  - `count`: number of matches per file.
- `-i` makes the search case-insensitive. `multiline` lets patterns span lines and `.` match newlines.
- `head_limit` keeps only the first N lines or entries of the output.
- Files ignored by .gitignore are skipped.
- Always use this tool for content search instead of running `grep` or `rg` through Bash.
