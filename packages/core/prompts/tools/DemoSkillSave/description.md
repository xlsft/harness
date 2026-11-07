Proposes a skill drafted from a demo recording. The user sees the whole SKILL.md in a review card, can edit it, choose where it goes and save it, or discard it with a note; nothing is written before they save. The result says what they did and where the file went, or what they said instead. Call it once per draft, after reading the recording with DemoRecording.

The client writes the frontmatter from `name` and `description`; `body` is the Markdown after it. Write the body in your own words, for a future session that has not seen the demo:
- **When to use it** and what it needs first (an app running, a file, a signed-in account the user provides).
- **Apps used**, with what each step needs from them.
- **Steps**, numbered, each a goal and how to reach it ("Open the Export dialog: File > Export, or ⌘E"), naming controls by their label rather than by pixel positions, which change between windows and screens.
- **Check** after each step: what should be visible if it worked (a dialog titled "Export", the file in Finder), so the skill's user can confirm before going on, and what to do if it is not.
- Prefer the most robust tool for each step: a shell command, a file edit or the Browser tools where they do the same job, computer use for what only the app's interface can do. Where the recording says an app's tier would not allow a step, write that step for another tool or as a step for the user.
- Never put secrets, passwords or personal data from the recording into the skill; use placeholders and say the user supplies them.

`scope` is where it is saved: `project` (the default) puts it in the project's `.harness/skills/<name>/SKILL.md`, `personal` in the user's own skills for every project. The name must be new.
