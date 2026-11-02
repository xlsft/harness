Asks the user one or more multiple-choice questions and waits for the answers.

Use it only when you cannot continue without a decision that belongs to the user and that you cannot settle from the request, the code or a sensible default: an ambiguous requirement, a choice between approaches with real trade-offs, a preference that changes what you build. Do not use it for:
- facts you can find out by reading code or running a command;
- choices with an obvious conventional answer (take it and mention it);
- permission to run a tool (the permission system handles that).

How to ask:
- Put 1 to 4 related questions in one call instead of asking one at a time.
- Each question has `question` (the full question, ending with "?"), `header` (a chip label of at most 12 characters, such as "Database" or "Scope"), 2 to 4 `options` and `multiSelect`.
- Each option has a `label` of 1 to 5 words and a `description` of what choosing it means.
- Set `multiSelect` to true only when several options can be chosen together.
- A free-text answer is always available to the user, so do not add an "Other" option.
- If you recommend an option, put it first and end its label with "(Recommended)".

The result has the user's answers; continue the task with them. If the session has no user attached, the call fails: make a reasonable assumption, say what it is, and continue.

In plan mode, use it to settle requirements before you finish the plan. Never use it to ask whether the plan is good or whether to proceed: the plan stays invisible to the user until ExitPlanMode shows it, and that call is the approval step.
