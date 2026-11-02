Presents your finished plan and asks the user to approve it. Use it only in plan mode, once the plan is complete.

- Pass the whole plan as Markdown in `plan`. It is saved to the session's plan file and shown to the user.
- A good plan is short enough to scan and concrete enough to execute:
  - Context: the problem and the intended result, in one or two sentences;
  - Approach: only the approach you recommend, not every alternative;
  - Changes: the files to change and what changes in each, plus existing functions or helpers to reuse (with paths). For a pattern repeated across many files, describe it once and name a few examples;
  - Steps in order;
  - Verification: how you will check the result (tests, build, running it);
  - Risks or open points, if any.
- Settle open questions with AskUserQuestion before calling this. Do not ask "is this plan OK?" in text or with AskUserQuestion; this tool is that question.
- Use it only for tasks that involve changing code. For research or explanations, just answer.
- If the user approves, plan mode ends and you start implementing. If they reject it, you stay in plan mode: revise the plan using their feedback and call ExitPlanMode again.
- If no user can approve (a non-interactive session), the plan is saved and you should stop and present it.
