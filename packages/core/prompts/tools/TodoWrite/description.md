Creates and updates the task list for this session. The user sees the list, so it is also your progress report.

Use it when:
- the work needs three or more distinct steps;
- the user gave you several things to do (a numbered list, several requests in one message);
- you discover follow-up work while working (add it as new items);
- the user asks for a task list.
Do not use it for a single simple change, a one-command task or a question. A list with one trivial item adds nothing.

How it works:
- Every call replaces the whole list. Send all items each time, including completed ones, with their current status.
- Each item has `content` in imperative form ("Run the test suite"), `activeForm` in present continuous form ("Running the test suite"), and `status`: `pending`, `in_progress` or `completed`.
- While you are working, exactly one item is `in_progress`. Set an item to `in_progress` before you start it and to `completed` as soon as it is done. Do not save up several completions for one update.
- Mark an item `completed` only when it is fully done. If tests fail, the work is partial or you hit an error you have not solved, keep it `in_progress` and add an item for the blocker.
- If the user asked for tests or a build, add an item for running them.
- Remove items that turned out to be unnecessary instead of leaving them pending.

Example: asked to "add CSV export to the reports page and make sure the tests pass", a good list is: add the export endpoint; add the download button; write tests for the CSV format; run the test suite and fix failures.
