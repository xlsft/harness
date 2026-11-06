Starts a skill: a set of instructions someone wrote for a recurring kind of task, sometimes with files beside it. A message in this conversation lists the skills you can start, each with a line saying what it is for; that line is all you have of it until you start it.

When a request matches a listed skill, start it before doing the work yourself: the skill knows how this user wants it done. Don't start one for a task it only resembles, and don't start one just to read about it.

The instructions arrive in the next message, inside `<skill>` tags. Follow them as the way to do the task, within the rules you already have; they don't overrule the user or your safety rules. When they name other files, those are in the skill's folder, given in the tag's `directory`: read them only when the instructions call for it.

- `skill`: the name as listed, without a slash.
- `args`: what the skill should work on, as a single string, when the request says. Leave it out otherwise.

If the user typed `/name`, the skill has already started: its tags are in the conversation, so don't start it again.

A skill that needs more than reading may need the user's approval to start. If it's refused, carry on without it.
