Returns the contents of a file on the local machine. It can read any file on the machine that the permissions allow; if the user gives you a path, assume it is valid and read it. Reading a path that does not exist just returns an error.

- `file_path` must be absolute. Relative paths are rejected.
- Returns up to 2000 lines from the start by default. For large files, pass `offset` (1-based first line) and `limit` (number of lines). Read small files whole instead of in pieces.
- Each output line starts with its line number and a tab. That prefix is not part of the file: never copy it into Edit's `old_string` or into Write's `content`.
- Lines longer than 2000 characters are cut.
- Images (PNG, JPEG, GIF, WebP) come back as images you can see, so use Read for screenshots the user points to. PDFs come back as documents when the model supports them. Other binary files are refused.
- A Jupyter notebook (.ipynb) comes back as its cells, each with its id, index, type, source and outputs; `offset` and `limit` then count cells, not lines. Images in outputs are named, not shown.
- An empty file returns a notice saying it is empty.
- It reads files only. To list a directory, use Glob or `ls` through Bash.
- When you need several files, read them in parallel in one response.
- You must read a file before you change it with Edit or NotebookEdit or overwrite it with Write.
