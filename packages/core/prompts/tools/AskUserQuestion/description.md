Asks the user one or more questions in a card and waits for the answers.

Use it only when you cannot continue without a decision that belongs to the user and that you cannot settle from the request, the code or a sensible default: an ambiguous requirement, a choice between approaches with real trade-offs, a preference that changes what you build. Do not use it for:
- facts you can find out by reading code or running a command;
- choices with an obvious conventional answer (take it and mention it);
- permission to run a tool (the permission system handles that).

How to ask:
- Put 1 to 4 related questions in one call instead of asking one at a time. They appear as steps of one card and are answered together.
- Each question has `question` (the full question, ending with "?"), `header` (a chip label of at most 12 characters, such as "Database" or "Scope") and `type`:
  - `single`: the user picks one of 2 to 6 `options`;
  - `multi`: the user picks several of 2 to 6 `options`; `min` and `max` limit how many (by default any number, including none);
  - `text`: the user types a free answer; no options;
  - `yes_no`: the user answers Yes or No; no options.
- Each option has a `label` of 1 to 5 words and optionally a `description` of what choosing it means. Set `recommended` on the option you suggest instead of writing it into the label.
- Questions with options always offer the user a free-text "Other" answer, so do not add an "Other" option. Set `allowOther` to false only when an answer outside the options makes no sense.

The answer:
- The result lists one entry per question, in order: the chosen labels in `selected`, free text in `other` (or `text` for text questions), or `skipped` when the user left it unanswered. Decide skipped questions yourself and say what you chose.
- The user may skip the whole card: then make reasonable choices yourself, state them and continue.
- The user may answer with a chat message instead; the result then carries that message as the free-form answer.
- Chat messages the user writes while the question is open reach you after the result; take them into account too.

If the session has no user attached, the call fails: make a reasonable assumption, say what it is, and continue.

In plan mode, use it to settle requirements before you finish the plan. Never use it to ask whether the plan is good or whether to proceed: the plan stays invisible to the user until ExitPlanMode shows it, and that call is the approval step.
