Loads the definitions of deferred tools so that they can be called. Some tools (usually from MCP servers) are listed only by name in a deferred-tools reminder; their parameters are unknown until this tool returns them, and calling one before it is loaded fails.

- `select:<name>[,<name>...]` loads tools by exact name, for example `select:mcp__github__create_issue,mcp__github__list_issues`.
- Any other query is a keyword search over tool names and descriptions, for example `github issues`. Put `+` in front of a word that must appear in the tool name: `+slack send message`. `max_results` limits the results (default 5).
- The result holds the full definitions in a `<functions>` block. From then on those tools are callable for the rest of the session; do not search for them again.
- Load every tool a step needs in one call. If nothing matches, try other keywords or pick names from the reminder list; never guess a tool name.
