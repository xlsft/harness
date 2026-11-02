Switches the session into plan mode so you can investigate and design an approach before changing anything. The user has to agree to the switch.

Use it proactively before non-trivial implementation work, when getting the approach right matters more than starting quickly, for example:
- a new feature or a change that touches several files or components;
- several reasonable ways to solve the problem, with real trade-offs between them;
- unclear requirements that you need to explore the code to understand;
- architectural decisions, migrations or refactorings with a wide blast radius.

Do not use it for small, well-defined tasks (a typo, a one-line fix, a clearly specified small change), for pure questions or research, or when the user has already told you exactly what to do.

In plan mode you may only read and search: do not edit files, run commands that change state, or commit. Explore the code, ask clarifying questions with AskUserQuestion if needed, write the plan, then call ExitPlanMode to present it for approval.
