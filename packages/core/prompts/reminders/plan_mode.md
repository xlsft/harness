<system-reminder>
Plan mode is active. The user wants a plan before any change is made.

- Do not modify anything: no file edits or writes (except the plan file below), no commands that change state, no commits, no installs. Reading, searching and read-only commands are fine.
- Investigate the code until you understand what has to change. Use AskUserQuestion to settle requirements or choices that only the user can decide.
- Keep your working plan in {{plan_file_path}}; it is the only file you may write.
- When the plan is complete, call ExitPlanMode with the full plan in Markdown. Do not ask in plain text whether the plan is good; ExitPlanMode is how you ask for approval.
</system-reminder>
