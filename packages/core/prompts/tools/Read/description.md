Reads a file from the local filesystem and returns its contents.

- `file_path` must be an absolute path. Relative paths are rejected.
- By default up to 2000 lines are returned, starting at the first line. For long files, pass `offset` (1-based line number to start at) and `limit` (number of lines) to read a window; prefer reading the whole file when it is small.
- Output uses `cat -n` style: each line is prefixed with its line number and a tab. Line numbers start at 1. The prefix is not part of the file; never include it in `old_string` or `content` for other tools.
- Lines longer than 2000 characters are truncated.
- Image files (PNG, JPEG, GIF, WebP) are returned as images you can look at. PDF files are returned as documents when the model supports them.
- Reading an empty file succeeds and returns a notice that the file is empty.
- This tool reads files, not directories. To list a directory use Bash with `ls`, or Glob.
- Call Read on several files in one response when you need more than one.
- You must Read a file before you Edit it or overwrite it with Write.
