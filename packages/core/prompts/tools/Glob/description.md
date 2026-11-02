Finds files whose paths match a glob pattern, such as `**/*.ts` or `src/**/test_*.py`.

- Searches under `path` (an absolute directory) or, when omitted, under the current working directory. Do not pass "undefined" or "null"; just omit the field.
- Returns matching file paths sorted by modification time, most recently modified first.
- Files ignored by the project's .gitignore are skipped.
- Use it to locate files by name. To search file contents use Grep.
- Run several Glob calls in one response when you need different patterns.
