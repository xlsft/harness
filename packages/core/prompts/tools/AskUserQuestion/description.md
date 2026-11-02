Asks the user one or more multiple-choice questions and waits for the answers.

Use it when you genuinely need the user's input to continue: to settle an ambiguous requirement, to choose between approaches with real trade-offs, or to confirm a preference that changes what you build. Do not use it for things you can find out yourself by reading the code or running a command, and do not use it to ask for permission to run a tool (the permission system handles that).

How to ask:
- Send 1 to 4 questions in one call. Group related questions together instead of asking them one after another.
- Each question has a `question` (the full question, ending with a question mark), a `header` (a very short label of at most 12 characters shown as a chip, for example "Database" or "Scope"), 2 to 4 `options` and a `multiSelect` flag.
- Each option has a short `label` (1 to 5 words) and a `description` explaining what choosing it means or implies.
- Set `multiSelect` to true only when several options can sensibly be chosen together.
- The user can always type a free-form answer instead of picking an option, so do not add an "Other" option yourself.
- If you recommend one option, list it first and append "(Recommended)" to its label.

The result contains the user's answer to each question. Continue the task using those answers. In plan mode, use this tool to clarify requirements before you finish the plan, but not to ask whether the plan is ready; ExitPlanMode is for that.
