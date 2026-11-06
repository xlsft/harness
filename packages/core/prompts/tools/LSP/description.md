Asks the language server of a file's language what it knows about the code: where a symbol is defined, where it is used, its type and documentation, and what a file or the workspace declares. It reads and changes nothing on disk.

Set `operation`:
- `definition`: where the symbol at `line` and `character` is defined.
- `references`: every use of the symbol at `line` and `character`, its declaration included.
- `hover`: the type and documentation of the symbol at `line` and `character`.
- `document_symbols`: the outline of `file_path`.
- `workspace_symbols`: symbols matching `query` across the project of `file_path`; any file of that language will do.

`file_path` is absolute. `line` and `character` count from 1, as Read shows them: point at a character inside the identifier. Results give paths relative to the working directory as `path:line:character`, also counted from 1, and are cut when very long.

The first call for a language starts its server, which can take a while on a large project. If the server is still indexing, the result says so instead of waiting for long; go on with Grep and Read, or try again shortly. If no server is installed for the language, the result names the one that would be needed; go on with Grep and Read, and do not install it unless the user asks.
