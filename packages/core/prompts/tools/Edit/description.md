Replaces exact text in an existing file.

- `file_path` must be an absolute path to a file you have already read with Read in this conversation; editing an unread file fails.
- `old_string` must match the file byte for byte, including whitespace and indentation. Copy it from the Read output without the line-number prefix (everything up to and including the first tab on each line).
- `old_string` must occur exactly once in the file, otherwise the edit fails. Include enough surrounding lines to make it unique, or set `replace_all` to true to replace every occurrence (useful for renaming).
- `new_string` must differ from `old_string`. Keep the file's existing indentation style.
- To create a file use Write. To make several changes to one file, call Edit several times or use a larger `old_string` that covers them.
