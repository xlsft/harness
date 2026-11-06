Records what happens in the browser as a GIF, so the user can watch the task afterwards.

Call it with `action: "start"` before the steps worth showing and `action: "stop"` after them. Stopping returns the file's path; send it to the user with SendUserFile if they asked to see it.

Only the browser's active tab is recorded, at a few frames a second. A recording stops collecting frames after ten minutes. Record when the user asks for one or a change is easiest to judge by seeing it; a recording of a page with passwords or personal data on it shows them.
