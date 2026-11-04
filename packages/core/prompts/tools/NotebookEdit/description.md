Changes one cell of a Jupyter notebook (.ipynb): replaces it, inserts a new one or deletes it. Only that cell is rewritten; the rest of the file keeps its exact bytes. Use this instead of Edit or Write on notebooks, whose JSON is easy to break.

- `notebook_path` must be absolute. Read the notebook first: Read shows each cell with its id, index, type and outputs. An edit is refused if the file was not read or has changed since.
- Address a cell with `cell_id` or, in older notebooks whose cells have no ids, with `cell_index` (0-based). Pass one of them, not both.
- `edit_mode` is `replace` (the default), `insert` or `delete`.
- `replace` sets the cell's whole source to `new_source`, its type to `cell_type`, or both. A replaced code cell loses its outputs and execution count, since they belonged to the old source; it does not run.
- `insert` needs `new_source` and `cell_type`. The new cell goes right after the addressed one, or first when no cell is addressed. The result gives its id.
- `new_source` is plain text, not JSON: no line lists, no escaping beyond what the language needs.
- This tool does not execute anything. To see new outputs, run the notebook through Bash if the user wants that.
