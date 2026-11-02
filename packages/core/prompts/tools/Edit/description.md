Replaces exact text in a file.

- `file_path` must be absolute, and you must have read the file with Read in this session. The edit fails if you have not, or if the file changed on disk since you read it; read it again in that case.
- `old_string` has to be identical to the text in the file, character for character, including indentation and blank lines. Copy it from the Read output without the line-number prefix (the number and the tab before the content).
- `old_string` must appear exactly once. Keep it small: usually one to three lines, just enough to be unique. If it matches several places, add a nearby line, or set `replace_all` to true to change every occurrence (useful for renames).
- `new_string` must differ from `old_string`. Keep the file's indentation style.
- An empty `old_string` on a path that does not exist creates the file with `new_string` as its content.
- For several changes in one file, make several Edit calls one after another.
