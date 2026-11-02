Writes a file to the local filesystem, creating it or replacing its entire contents.

- `file_path` must be an absolute path. Missing parent directories are created.
- If the file already exists you must have read it with Read earlier in this conversation; otherwise the call fails. This prevents overwriting changes you have not seen.
- Prefer Edit for changing part of an existing file; use Write for new files or complete rewrites.
- Do not create new files unless they are needed for the task. Never create documentation or README files unless the user asked for them.
- Write the exact file contents. Do not include line-number prefixes from Read output.
