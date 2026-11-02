Creates and updates the task list for the current session. The list is shown to the user, so it doubles as a progress report.

When to use it:
- the task needs three or more distinct steps, or is complex enough that tracking helps;
- the user gave you several things to do (a numbered list, comma-separated requests);
- new follow-up work appears while you are working.
Skip it for a single simple step, for trivial tasks and for pure questions; a list with one item adds nothing.

How it works:
- Every call replaces the whole list. Always send all items, including completed ones, with their current status.
- Each item has `content` (imperative form, e.g. "Run the test suite"), `activeForm` (present continuous form shown while it is being worked on, e.g. "Running the test suite") and `status`: `pending`, `in_progress` or `completed`.
- Keep exactly one item `in_progress` at a time while you are working. Mark an item `in_progress` before you start it and `completed` as soon as it is done; do not batch several completions into one update.
- Mark an item completed only when it is fully done. If tests fail, the work is partial or you hit an error, keep it `in_progress` and add an item for what is blocking you.
- Remove items that turned out to be irrelevant instead of leaving them pending.
