Attaches a pull request (a merge request on GitLab) to this session. The client then follows its state, and the user sees it in the session's details. Binding another PR replaces the current one. Reading PR state does not need this; bind when the user wants this session to keep track of the PR, or before PrMonitor when the PR is not the current branch's.

The binding belongs to the session and lasts until PrUnbind. Following stops by itself when the PR is merged or closed.
