Executes the code cells of a Jupyter notebook and writes their outputs and execution counts into the file. Every other byte of the notebook stays as it was. The result shows each cell that ran with its new outputs.

Each run starts a fresh kernel: there is no state from an earlier run. Without `cell_id` or `cell_index` it runs every code cell, in order; with one of them it runs from the first cell through that one, since a cell usually needs what comes before it. A cell that fails stops the run, as Run All does: the outputs so far are written, with the traceback in the failing cell, and the result is marked as an error.

It runs arbitrary code with the user's Python environment, so it goes through the same permission check and sandbox as Bash. A kernel needs a local port and may need the network; if the sandbox stops it, say so before asking with `run_outside_sandbox`.

It needs `nbclient`, `nbformat` and a kernel (`ipykernel`) in the interpreter it uses: `python` if you pass it, else `.venv` or `venv` in the working directory, else `python3`. If they are missing, the result says what to install; ask the user before installing anything.

Use NotebookEdit to change cells first, and Read to look at outputs later. Images in outputs are written to the file and named in the result, not shown.
