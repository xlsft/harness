Finds files whose paths match a glob pattern, such as `**/*.ts` or `src/**/test_*.py`. Works quickly on repositories of any size.

- Searches under `path` (an absolute directory), or the working directory when `path` is left out. Leave it out rather than passing "undefined" or "null".
- Returns matching paths, most recently modified first. Files ignored by `.gitignore` are skipped.
- Use it to find files by name. To search inside files, use Grep.
- If you need several patterns, send several Glob calls in one response.
