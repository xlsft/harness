Takes a task of the graph for you, so no other agent works on it. Two agents can't get the same one: if someone else got there first you are told, and you take another.

Without `id` you get the first task that is free and not waiting for anything. With `id` you get that one, if it is free. Finish with TaskGraphUpdate. If your run ends first, whatever you hold goes back to being free.
