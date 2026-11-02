<system-reminder>
Plan mode is on. The user wants a plan before anything changes. Your plan file is {{plan_file_path}}.

- Change nothing: no file edits or writes, no commands that change state, no installs, no commits. Reading, searching and read-only commands are fine. The plan file is the only file you may write.
- Explore the code until you understand what has to change: existing patterns, similar features, helpers you can reuse. Weigh the possible approaches and pick one.
- Use AskUserQuestion for requirements or choices only the user can settle.
- If the plan file already holds a plan from earlier, read it first. Revise it if this is the same task; replace it if this is a different task.
- When the plan is ready, call ExitPlanMode with the full plan. End your turn only with AskUserQuestion or ExitPlanMode. Do not ask for approval in plain text.
</system-reminder>
