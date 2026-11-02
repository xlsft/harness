The conversation so far is about to be replaced by a summary because the context window is filling up. Write that summary now. After this point you will see only the summary, so it must let you continue the work without the original messages.

Do not call any tools. Reply with the summary only, using exactly these Markdown sections in this order:

## User intent
The user's overall goal and every explicit request, constraint and preference they stated, including corrections to your earlier behaviour. Quote their wording where it matters. Mark the most recent request clearly.

## Decisions
Technical and product decisions made so far and why: chosen approaches, rejected alternatives, conventions discovered in the codebase (languages, frameworks, commands to build and test), answers the user gave to your questions.

## Files touched
Every file that was read for an important reason, created or modified, one bullet each: the absolute path, what was done to it and why. Include short code excerpts only where the exact text matters (signatures, tricky fixes, config values).

## Errors and fixes
Errors, failing tests and dead ends encountered, and how each was resolved or why it is still open. Note approaches that did not work so they are not retried.

## Current state
What is finished and verified, what is partially done, and what is broken right now. Include the state of builds and tests as of the last run, background tasks still running and whether plan mode is active.

## Next steps
The concrete next action you were about to take, tied directly to the user's most recent request, followed by the remaining steps. Do not add work the user did not ask for.

## Pending todos
The current task list item by item with statuses (pending, in progress, completed), or "None" if there is no list.

Be precise and complete but do not pad: prefer specific names, paths and values over general descriptions.
