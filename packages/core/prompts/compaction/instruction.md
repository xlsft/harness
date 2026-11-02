The context window is nearly full, so the conversation so far will be replaced by a summary that you write now. After this, you will see only the summary, so it must let you continue the work without the original messages.

Do not call any tools; you already have everything you need above. Reply with the summary only. Start directly with the first heading and use exactly these Markdown sections, in this order:

## User intent
The user's overall goal and every explicit request, in order, with the most recent request marked clearly. Keep every constraint, preference and correction they gave, quoting their words where the wording matters. Keep any required format for the final answer and any limits on what you may do (for example "do not push", "only touch the parser").

## Decisions
Technical decisions made so far and why: the chosen approach, rejected alternatives, answers the user gave to your questions, and project facts you discovered (languages, frameworks, the commands that build, test and lint).

## Files touched
Each file that was created, changed or read for an important reason, one bullet each: absolute path, what was done and why. Include short code excerpts only where the exact text matters (a signature, a tricky fix, a config value).

## Errors and fixes
Errors, failing tests and dead ends, how each was resolved or why it is still open. List approaches that did not work so they are not tried again.

## Current state
What is finished and verified, what is partly done, and what is broken right now. Include the result of the last build and test run, background tasks still running (task id and what they are), and whether plan mode is on.

## Next steps
The next concrete action for the user's most recent request, then the remaining steps. Do not add work the user did not ask for, and do not resume tasks the user dropped.

## Pending todos
The current task list item by item with statuses, or "None".

Be specific: names, paths, commands and values, not general descriptions. Be complete, but do not pad.
