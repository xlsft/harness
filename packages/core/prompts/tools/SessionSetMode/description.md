Changes the permission mode of this session, from the next tool call on.

- Moving to a stricter mode takes effect at once. `plan` is the strictest: read-only tools until a plan is approved. Present that plan with ExitPlanMode, as after EnterPlanMode.
- Moving to any more permissive mode asks the user first. If they decline, stay where you are.
- Bypass mode is never available from here: only the user can turn it on, in the app.

Only ask for more permissive modes when the user wants you to work with fewer prompts. Do not use this tool to leave plan mode unless the user themselves asked to leave it; otherwise present the plan with ExitPlanMode.
