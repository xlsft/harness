The user wants this done by splitting it up, working from {{cwd}}:

{{arguments}}

Read enough to find the seams first. Then decide: if the pieces don't really stand apart, or the whole thing is small, do it in this conversation and say why. Otherwise put the pieces in the task graph with their dependencies and run what is independent side by side. Use plain agents for a few pieces, with `isolation: "worktree"` when they edit the same tree. Use a team when pieces affect each other, and the Workflow tool when there are many or the steps follow a fixed shape.

You stay responsible for the result. Give each agent its goal, its boundaries and how to tell it's done. Read each report as work to check, bring the changes together, resolve what conflicts, and run the checks the project has on the whole. Tell the user what was done, what wasn't, and what you could not verify.
