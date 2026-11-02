Writes a file, creating it or replacing everything in it.

- `file_path` must be absolute. Missing parent directories are created.
- To overwrite an existing file you must have read it with Read in this session, and it must not have changed since. Otherwise the call fails, which protects changes you have not seen.
- Use Edit to change part of an existing file; it sends only the change. Use Write for new files or complete rewrites.
- Create files only when the task needs them. Never create documentation or README files unless the user asked.
- `content` is the exact file text: no line-number prefixes from Read, no emojis unless asked.
