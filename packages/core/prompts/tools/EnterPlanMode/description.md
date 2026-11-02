Asks to switch the session into plan mode, where you investigate and design an approach before changing anything. The user has to accept the switch.

Use it before implementation work that is not small, because agreeing on the approach first avoids redoing work. For example:
- a new feature with open questions ("add report export": which formats, generated on request or in a job?);
- several reasonable approaches with real trade-offs (sessions in the database or in signed cookies, a new table or a JSON column);
- a change that touches more than a few files or alters existing behavior;
- unclear requirements you must explore the code to understand ("reduce startup time", "find out why uploads sometimes fail");
- architecture choices, migrations or wide refactors.
If you would otherwise ask the user how to approach the task, plan instead: investigate, then lay out the options with what you learned.

Do not use it for:
- small, well-defined changes (a typo, an obvious bug, a single well-specified function);
- work the user has already described step by step;
- questions, explanations and research ("where is the config loaded?").

In plan mode you only read, search and run read-only commands. You may ask questions with AskUserQuestion, then you call ExitPlanMode with the finished plan.
