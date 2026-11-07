Loads the definitions of deferred tools so that they can be called. Rarely used built-in tools and, when there are many, MCP tools are deferred: a reminder lists each by name with a one-line summary, and their parameters stay unknown until this tool returns them.

- `select:<name>[,<name>...]` loads tools by exact name, for example `select:PrStatus,PrMonitor` or `select:mcp__github__create_issue`.
- Any other query is a keyword search over tool names and descriptions, for example `github issues`. Put `+` in front of a word that must appear in the tool name: `+slack send message`. `max_results` limits the results (default 5).
- The result holds the full definitions in a `<functions>` block. From then on those tools are callable for the rest of the session, after compactions too; do not search for them again.
- Load every tool a step needs in one call. If nothing matches, try other keywords or pick names from the reminder list; never guess a tool name.
