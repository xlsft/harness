Presents your implementation plan to the user and asks them to approve it. Call it only in plan mode, once the plan is complete.

- Pass the whole plan as Markdown in `plan`. It is saved to the session's plan file and shown to the user, who either approves it or asks you to keep planning.
- A good plan is concrete and short enough to review: the goal in one or two sentences, the files to change and what changes in each, the order of the steps, how you will verify the result (tests, build, manual checks), and any risks or open decisions.
- Resolve open questions first (with AskUserQuestion) instead of leaving them in the plan. Do not use AskUserQuestion to ask "is this plan OK?"; this tool is that question.
- Only use it for tasks that involve writing code. For research or explanation tasks, just answer.
- If the user approves, plan mode ends and you can start implementing. If they reject it, you stay in plan mode: revise the plan using their feedback and call ExitPlanMode again.
