Records the results of a code review as a typed list the user sees in the transcript, ordered by severity, with each item linking to the file at its line. Call it once a review is done, instead of writing the findings out as prose, then say in your message what stands out. If the review found nothing, call it with an empty list: that reads as "no findings".

The client ranks severity from `category` and `verdict`: security and data loss first, then crashes, wrong behavior and concurrency, then performance and reliability, then maintainability, style, docs and tests. An uncertain finding ranks one step lower.

To update a finding, for instance after fixing it, report it again with the same `file` and `summary` and the new `outcome`. Findings you do not repeat stay on the list as they were, and an empty list adds nothing, so report only what changed.

For each finding:
- `file`, `line`: where it is; `line` is 1-based, or 0 for the file as a whole.
- `summary`: what is wrong, in a sentence or two.
- `failure_scenario`: the concrete input or sequence of events that makes it go wrong, and what the user then sees. If you cannot write one, you probably do not have a finding.
- `category`: `security`, `data-loss`, `crash`, `correctness`, `concurrency`, `performance`, `reliability`, `error-handling`, `compatibility`, `maintainability`, `style`, `docs` or `tests`.
- `verdict`: how sure you are: `confirmed` (you traced or reproduced it), `likely`, `uncertain`, `rejected` (you checked and it is not a problem).
- `outcome`: `open` (the default), `fixed`, `wont_fix` or `deferred`.
- `short_summary`: a label of a few words for the collapsed row.

`level` says how deep the review went: `quick`, `standard` or `deep`.
