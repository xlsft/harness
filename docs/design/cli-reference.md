# CLI reference: Claude Code terminal UI (structure and UX)

This is a layout and behavior reference for building `packages/cli` (a Go TUI built with the
charm.land toolchain). It records how Claude Code's terminal UI is **structured** and how it
**behaves**, so our TUI can match it one-to-one on layout, spacing, information density, key
bindings and state transitions.

**Identity is off-limits.** This document does *not* specify Claude Code's spinner glyphs or
animation frames, spinner verbs, brand/accent hues, logo, mascot or welcome art. Where such an
element exists it is shown as a placeholder (`⟨spin⟩`, `⟨verb⟩`, `⟨logo⟩`, `⟨brand⟩`) and marked
**OWN DESIGN**. Color is described by **role** ("secondary text", "diff add background"). The
observed 256-color values are listed only for non-brand roles, as a hint for contrast and
lightness. Pick our own palette.

**Our overrides** live in [brand.md](brand.md): palette, logo, loaders and glyphs. In particular, the message/tool bullet `⏺` becomes `▰` and the result connector `⎿` becomes `└`; status words are plain (`Thinking`, `Running <tool>`) instead of rotating verbs.

- Source: `claude` v2.1.220 (macOS), driven through a pseudo-terminal and a VT emulator (pyte),
  with text and SGR attributes captured. Models: Haiku 4.5 and Sonnet 5.
- Terminal sizes: 120x40 (main) and 80x24. Default theme is dark. Light theme, `TERM=xterm`
  (16 colors) and `NO_COLOR=1` were also checked.

Legend for mocks: `⟨spin⟩` animated spinner cell, `⟨verb⟩` animated status word,
`⟨done⟩`/`⟨past-verb⟩` the glyph and word on the turn-complete line, `⟨logo⟩` mascot art,
`⟨brand⟩` brand-colored text. `·` is a literal middle dot separator (U+00B7). `⎿` (U+23BF) is the
result connector. `⏺` (U+23FA) is the message/tool bullet.

---

## 0. Screen anatomy (applies to every state)

Claude Code renders as a full-height, inline app. The transcript scrolls in the upper part.
A **bottom dock** stays anchored to the last rows of the terminal. On resize the whole
transcript is re-rendered (re-wrapped) for the new width.

```
<transcript: messages flow top→bottom, older lines scroll off into terminal scrollback>
...
⟨spin⟩ ⟨verb⟩… (12s · ↓ 1.1k tokens · thinking)        ← working line (only while busy)
  ⎿  Tip: <rotating tip text>                         ← optional tip under working line
  3 tasks (1 done, 1 in progress, 1 open)             ← pinned task list (if any, ctrl+t)
  ◼ Fix add bug
                                       ● high · /effort   ← right-aligned hint row (contextual)
──────────────────────────────────────────────────── session-name ──   ← input top rule (+ optional label)
❯ <input text>                                        ← input (1..n rows)
────────────────────────────────────────────────────────────────────   ← input bottom rule
                                                      ← blank row
  ⏸ manual mode on · ← for agents                     ← footer / mode line
  ⏺ main                                              ← agent panel (only when subagents exist)
  ◯ Explore  Find calc.py and existing test patterns            23s · ↓ 20.0k tokens
```

Key structural facts:

- **The input "box" is not a box.** It is two full-width horizontal rules (`─`) with no side
  borders and no corners. The prompt glyph `❯` sits in column 0, followed by one space. Text
  starts in column 2, and wrapped or continued lines are indented to column 2.
- Rule color is the "subtle border" role (dark: `#808080`). When the session has a name or color,
  both rules take the session color and the name appears as a reverse-video label inset into the
  top rule, 2 cells from the right edge: `──── add-sub-function ──`.
- The **footer** row is column 2, secondary color. It shows the mode indicator and contextual hints
  joined with ` · `. It is replaced by other content (the shortcut help, shell mode hint, the
  transcript-mode footer) depending on state.
- **Modal UI replaces the input region.** Permission prompts, the plan approval, AskUserQuestion,
  the rewind menu, and the settings or model dialogs take the place of the input rules and the
  footer. Elements pinned below (task list, agent panel) stay below the modal.
- **Popup menus (`/`, `@`) render *above* the input top rule**, at most 5 rows. The footer stays.
- Transcript blocks are separated by exactly one blank line. Body text is indented 2 columns
  (after `⏺ ` or 2 spaces). Tool results hang from `  ⎿  ` (2 spaces, `⎿`, 2 spaces), so result
  text starts at column 5.

---

## 1. Startup / welcome

### 1a. Workspace trust prompt (first run in a folder)

```
────────────────────────────────────────────────────────────────────────────────────────  (warning color rule)
 Accessing workspace:                                                       (warning color, bold)

 /full/path/to/project                                                      (bold, wraps at width-1)

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source
 project, or work from your team). If not, take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide                                                             (secondary)

 ❯ 1. Yes, I trust this folder                                              (❯ + label = accent; "1." secondary)
   2. No, exit

 Enter to confirm · Esc to cancel                                           (secondary)
```

A second one-time dialog uses the same component, with an accent-colored rule and a bold accent
title (for example "Claude in Chrome extension detected"). Its footer hint is italic secondary.

### 1b. Welcome screen (120 cols)

```
╭─── ⟨brand⟩Claude Code⟨/brand⟩ v2.1.220 ───────────────────────────────────────────────────────────────────╮
│                                                    │ Tips for getting started                             │
│                 Welcome back <user>!               │ Run /init to create a CLAUDE.md file with instruc…   │
│                                                    │ ──────────────────────────────────────────────────── │
│                      ⟨logo⟩                        │ What's new                                           │
│                      ⟨logo⟩                        │ Bug fixes and reliability improvements               │
│                      ⟨logo⟩                        │ Added gateway spend-limit support to Claude Code's … │
│   Haiku 4.5 · Claude Max · <email>'s               │ Added a workspace trust prompt to `claude agents` …  │
│   Organization                                     │ /release-notes for more                              │
│                 /…/scratchpad/proj                 │                                                      │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────╯

 ⚠ 2 MCP servers need authentication · run /mcp

<empty rows: dock anchored to bottom>

────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────────────────────────────────

  ⏸ manual mode on · ← for agents
```

Layout facts:

- A rounded box (`╭╮╰╯`) spanning the full width. The **title is inset into the top border**:
  `╭─── <app name> <version> ───…╮`. The version is secondary.
- Two columns. The left column is a fixed 52 cells, centered: greeting (bold), logo, then a
  `model · plan · org` line (secondary, wraps), then the cwd shortened with `/…/` (secondary). A
  vertical `│` divider (dim) sits at column 53. The right column fills the rest and holds stacked
  sections, each with a bold heading ("Tips for getting started", "What's new"). Sections are
  separated by a `─` rule. Every line is truncated with `…` to the column width. The last line is
  italic secondary ("/release-notes for more").
- At 80 cols the left column stays 52 wide and the right column shrinks to about 23. Headings wrap
  ("Tips for getting / started"), and the bullet items truncate hard (`Run /init to create a …`).
- Below the box: startup notices, one per line, at column 1. Example: `⚠` + text in the warning
  color, then ` · run /mcp` in secondary.
- No placeholder text in the empty input. A dim ghost *prompt suggestion* appears after a turn
  completes (see 2f).
- The box border and title use the brand color: **OWN DESIGN**. The box, the two-column layout and
  the info lines are structure we can copy.

### 1c. Footer / status line

| Mode | Footer text | Mode-text role |
|---|---|---|
| default ("manual") | `⏸ manual mode on · ← for agents` | secondary (no special color) |
| accept edits | `⏵⏵ accept edits on (shift+tab to cycle) · ← for agents` | "accept-edits" color (dark: lavender `#afafff`), rest secondary |
| plan | `⏸ plan mode on (shift+tab to cycle) · ← for agents` | "plan" color (dark: teal `#5fafaf`) |
| auto | `⏵⏵ auto mode on (shift+tab to cycle) · ← for agents` | "auto" color (dark: yellow `#ffd700`) |

- The `· ← for agents` hint disappears while the input has text.
- Only the colored part (glyph + "X mode on ") takes the mode color. `(shift+tab to cycle)` is
  secondary.
- If the current model doesn't support a mode, a right-aligned warning appears in the row above
  the input: `auto mode unavailable for this model` (warning color).
- A user-configured status line (command output) would render here as well. In our capture it
  printed nothing.

---

## 2. Typing and input affordances

### 2a. Multiline input

```
                                                                                  ctrl+g to edit in Vim
────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ first line
  second line via backslash
  third line via meta-enter, and now a very long line that should wrap around the edge of the 120 column
  see how wrapping indentation works in the prompt box
────────────────────────────────────────────────────────────────────────────────────────────────────────

  ⏸ manual mode on
```

- Newline: `\` then Enter (the backslash is consumed). Alt/Meta+Enter (ESC CR) also works.
  Shift+Enter wasn't tested.
- Continuation and soft-wrapped lines are indented 2 (aligned under the text, not the `❯`). The box
  grows upward and the dock pushes the transcript up.
- Once the input is multiline, a right-aligned secondary hint `ctrl+g to edit in <$EDITOR name>`
  appears in the row directly above the top rule.

### 2b. `?` shortcut help (typed into empty input)

The footer row turns into a 3-column help grid (secondary color). The input stays visible.

```
────────────────────────────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────────────────────────────
  ! for shell mode        double tap esc to clear input      ctrl + shift + _ to undo
  / for commands          shift + tab to auto-accept edits   ctrl + z to suspend
  @ for file paths        ctrl + o for verbose output        ctrl + v to paste images
  /btw for side question  ctrl + t to toggle tasks           opt + p to switch model
                          backslash (\) + return (⏎) for     ctrl + s to stash prompt
                          newline                            ctrl + g to edit in $EDITOR
                                                             /keybindings to customize
```

Columns start at x=2, x=26 and x=61 (at 120 cols). The text wraps inside its column. Backspace
removes the `?` and restores the footer.

### 2c. `/` command menu

```
  /artifact-design                            Design guidance and fundamentals for Artifacts.      ← selected (accent)
  /add-dir                                    Add a new working directory
  /advisor                                    Let Claude consult a stronger model at key moments
  /agents                                     (removed) Ask Claude to create/manage subagents, …
  /artifacts                                  Browse your published and shared artifacts
────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ /
────────────────────────────────────────────────────────────────────────────────────────────────────────

  ⏸ manual mode on
```

- Placed **above** the input. 5 visible rows, a sliding window that scrolls as the selection
  moves. No border, no header, no scrollbar.
- Row layout: 2-space indent, then the command name in a fixed column (44 cells at 120 cols,
  about 32 at 80 cols), then the description. At 120 cols long descriptions are truncated. At 80
  cols they **wrap**, with the wrapped part aligned under the description column, so a row can take
  2 lines.
- Selected row: the whole row (name and description) in the **accent** color. No reverse video, no
  `❯` marker. Unselected rows: the whole row in secondary color.
- Filtering (`/co`): fuzzy/substring. Matched characters are highlighted in accent inside both the
  name and the description (`/`**`co`**`lor … Set the prompt bar `**`co`**`lor …`).
- `Tab` completes the selected command into the input (`❯ /copy`). `Enter` runs it. `↑/↓` moves.
  `Esc` closes the menu (the input text stays).
- Under NO_COLOR the selected row can't be told apart from the others. Our TUI should keep a
  non-color selection cue (see 10).

### 2d. `@` file mention menu

```
  + README.md                                     ← selected (accent)
  + test_calc.py
  + calc.py
  + .git/
  ◇ claude.ai Google Drive:googledrive://file/ – Retrieves Google Drive file content by file ID.
────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ look at @
```

- Same slot and size as the `/` menu (5 rows above the input). The prefix glyph shows the kind:
  `+` for a file or directory (directories end with `/`), `◇` for an MCP resource
  (`server:uri – description`).
- Filtering by the typed suffix (`@te` → `test_calc.py`, …). The list can include absolute paths
  outside the project (`/Users/<user>/.claude/`).
- After submit, mentions in the user's message and file references in assistant text
  (`calc.py:2`) render in the accent color.

### 2e. `!` shell mode

Typing `!` as the first character switches the input into shell mode:

```
────────────────────────────────────────────────────────────────────────────────────────────────────────   (shell color)
!                                                                                                          (shell color)
────────────────────────────────────────────────────────────────────────────────────────────────────────   (shell color)
  ! for shell mode                                                                                         (shell color)
```

- The prompt glyph `❯` becomes `!`. Both rules, the prompt and the footer take the "shell mode"
  color (dark: pink `#ff87af`). The footer text becomes `! for shell mode`.
- On Enter the command runs locally. The transcript shows:

```
! seq 1 30                      ← full-width row, shell-message bg (dark #5f5f5f), "!" in shell color
  ⎿  1
     2
     …                          (output is shown in full, not truncated, in this version)
     30
```

- The model then receives the output and usually comments on it (a normal turn follows).

### 2f. Other input behaviors

- **Prompt suggestion (ghost text).** After a turn, a dim suggested next prompt is shown in the
  empty input (`❯ fix it`, `❯ commit this`, `❯ run the tests`). Typing replaces it. Tab accepts it
  into the input, then Enter submits. Enter on an empty input with only the ghost text does
  nothing.
- **Esc with text.** The first Esc shows a right-aligned secondary hint `Esc again to clear` above
  the input. The second Esc clears the input.
- **ctrl+u** deletes the input and shows a right-aligned hint `Ctrl+Y to paste deleted text`.
- **`#` memory shortcut: not present** in this version (it isn't in the shortcut list).
  `/btw` (side question) is listed instead.

---

## 3. Streaming assistant output and the working indicator

### 3a. Working line

```
❯ look at @te!ls                                     ← user message (see 3c)

⏺ I'm not entirely sure what @te!ls refers to. …     ← assistant text streams in place

⏺ Reading 2 files, listing 1 directory…              ← live tool group (see 4)
  ⎿  calc.py

⟨spin⟩ ⟨verb⟩… (13s · ↓ 1.1k tokens · thinking)     ← working line
  ⎿  Tip: Run /install-github-app to tag @claude right from your Github issues and PRs
```

- Position: the last transcript row, directly above the dock, separated by one blank line from the
  content above and from the input rule below.
- Column 0: a 1-cell animated spinner, then a space, then an animated status word ending in `…`.
  Both are **OWN DESIGN**. Claude Code uses a brand-colored glyph cycle, a shimmer sweep across the
  word, and whimsical verbs. The word changes per turn.
- Then a secondary parenthesized stats group, with parts joined by ` · `:
  - elapsed time: `0s`, `13s`, `2m 1s`
  - output tokens with a down arrow: `↓ 479 tokens`, `↓ 1.1k tokens` (shown once tokens arrive)
  - phase: `thinking` (pulses between gray shades) while reasoning, then `thought for 3s` after
    reasoning ends. Other phases seen: `running stop hook` (inserted first), e.g.
    `(running stop hook · 8s · ↓ 953 tokens)`.
- Optional second line: `  ⎿  Tip: …` (secondary; command names inside are accent). It rotates
  each turn.
- There is **no "esc to interrupt" text** in this version. Interrupt is implicit (Esc).
- When background agents are pending and the main loop is idle, the line becomes
  `⟨spin⟩ Waiting for 1 background agent to finish`.

### 3b. Turn-complete line

```
⟨done⟩ ⟨past-verb⟩ for 15s
```

Replaces the working line when the turn ends. The whole line is secondary, with a static glyph and
a past-tense verb (**OWN DESIGN** wording) plus the total duration (`for 9s`, `for 2m 1s`). It stays
in the transcript as the turn footer.

### 3c. User message block

```
❯ Use the Write tool to create notes.txt containing lines "line 1" through "line 30". Then run with Bash: python3 -c
  "for i in range(60): print(i)"
```

- A full-width background band (dark `#3a3a3a`, light theme: a light gray) across every row of the
  message, including the padding to the right edge.
- `❯ ` in a low-contrast gray on the band (dark `#4e4e4e`). The text is the primary foreground.
  Wrapped lines are indented 2 and keep the band.
- Slash-command invocations look the same (`❯ /context`), with the command output hanging below
  via `⎿`.

### 3d. Markdown rendering

```
⏺ Markdown Sample                                ← H1: bold + italic + underline

  Features                                       ← H2: bold

  Quick Start                                    ← H3: bold

  - Install dependencies                         ← bullets "-", nested indent +2
    - Run npm install                            ← inline code: accent color, no backticks, no bg
  - Start coding

  1. Clone the repo
  2. Open in editor

  def hello(name):                               ← fenced code: syntax-highlighted, no fence, no border,
      return f"Hi {name}"                          no background, same 2-col indent as text

  Use const x = 5 for variables. Make text bold, italic, or ~~strike~~ through.
                                                 ← bold = bold, italic = italic, ~~ NOT rendered (literal)
  Visit Claude (https://claude.ai)               ← link: "text (url)"

  ┌──────────┬────────┐                          ← tables: single-line box drawing,
  │ Feature  │ Status │                            separator row between EVERY row,
  ├──────────┼────────┤                            1 space cell padding, header not bold
  │ Complete │ ✓      │
  ├──────────┼────────┤
  │ Pending  │ ✗      │
  └──────────┴────────┘

  ▎ This is important information to remember.  ← blockquote: "▎ " gutter, italic text

  ---                                            ← horizontal rule rendered literally
```

- The first line of an assistant message carries the `⏺ ` bullet (primary foreground for prose).
  Every following line is indented 2.
- Text is re-flowed to the terminal width minus the indent. Words are never broken except in long
  unbreakable tokens (paths), which hard-wrap.
- Code highlighting uses the 16-color ANSI palette (keywords blue, function names yellow/brown,
  comments green, strings red) for code blocks inside messages. Diffs use a 256-color theme
  (see 4c).
- **Our improvements (optional):** render `~~strike~~` and `---` properly. Claude Code does not.

### 3e. Message metadata (transcript mode only)

In ctrl+o transcript mode, each assistant message is preceded by a right-aligned secondary line
`04:51 PM claude-haiku-4-5-20251001` (time + model id).

---

## 4. Tool calls

### 4a. Collapsed tool groups (default view)

Version 2.1.220 **groups consecutive read-only/search/shell tools into one summary line** instead of
showing one block per tool.

While running:

```
⏺ Searching for 1 pattern, reading 1 file, listing 1 directory, running 2 shell commands…
  ⎿  $ ls -1 *.py                       ← the most recent call's detail (command or file path)
```

When done:

```
  Searched for 1 pattern, read 1 file, listed 1 directory, ran 2 shell commands
```

- Running: a blinking `⏺` (secondary; it alternates between visible and blank), a present
  participle summary ending in `…`, and a `⎿` line with the latest item (`$ cmd` for Bash, a
  filename for Read). Long commands hard-wrap at column 5.
- Done: **no bullet** (2-space indent), past tense, all secondary. **Counts are bold** (secondary
  bold): `Read **2** files, listed **1** directory`.
- Verb vocabulary: Read/reading N file(s), Listed/listing N director(y/ies), Searched/searching
  for N pattern(s), Ran/running N shell command(s). Parts are joined with `, `.
- Rejected or failed calls inside a group are not called out in the collapsed view. They are only
  visible in transcript mode (red bullet).

### 4b. Edit / Write blocks (never collapsed)

```
⏺ Update(calc.py)                           ← bullet = success color; tool name bold; "(path)" normal
  ⎿  Added 1 line, removed 1 line           ← counts bold
      1  def add(a, b):
      2 -    return a - b                   ← removed: red-tinted bg across full width
      2 +    return a + b                   ← added: green-tinted bg across full width
      3
      4
      5  def mul(a, b):
```

```
⏺ Write(notes.txt)
  ⎿  Wrote 30 lines to notes.txt            ← "30" and "notes.txt" bold
       1 line 1
       2 line 2
       …
      10 line 10
     … +20 lines                            ← secondary; first 10 lines shown, then "… +N lines"
```

- Header: `⏺ ToolName(arg)`. The bullet takes the status color: success green, error/rejected
  red-pink, in-progress blinking secondary. The tool name is bold. The argument is a
  cwd-relative path in the default view and the absolute path in transcript mode (wrapped with an
  8-col hanging indent: `⏺ Update(/very/long/…` / `        d4e-…`).
- Tool display names: `Update` (Edit), `Write`, `Read`, `Bash`, `Explore`/agent type for
  subagents.
- Summary line under `⎿`: `Added N lines, removed M lines` / `Wrote N lines to <file>` /
  `Read N lines` / `User rejected update to <file>` (the diff is still shown under the rejection).
- Diff body: indented to column 5. Line numbers are right-aligned (width = digits of the max line
  number, plus 1 leading space). Then a 1-char marker column (` `, `-`, `+`), then the code. Context
  line numbers are **dim**. Hunks are separated by a line containing `...` in the number column.
- Write preview: line numbers, no marker column, no background. It truncates after 10 lines with
  `… +N lines`.

### 4c. Diff color roles (dark theme observed values)

| Element | Attribute |
|---|---|
| Removed line, whole row | bg dark red `#5f0000` |
| Removed line number + `-` marker | fg red `#d75f5f` on the removed bg |
| Added line, whole row | bg dark green `#005f00` |
| Added line number + `+` marker | fg green `#5fd75f` on the added bg |
| Changed characters within an added line (word diff) | brighter green bg `#008700` |
| Code text on diff rows | primary fg plus syntax colors (256-color theme) |
| Context line number | dim |
| Light theme | removed bg `#ffd7d7`, marker `#d70000`; added bg `#d7ffd7`, marker `#00875f`; word diff `#afffaf`; text `#303030` |

Diff colors **ignore NO_COLOR and 16-color mode**. They always use 256-color values (see 10).

### 4d. Transcript / verbose mode (ctrl+o)

```
⏺ Update(/abs/path/…/proj/calc.py)
  ⎿  Added 1 line, removed 1 line
      …diff…
  ⎿  2 PostToolUse hooks ran                ← hook summary (count bold, hook name bold)

⏺ Bash(python -m pytest test_calc.py -v)    ← red bullet: failed or rejected, no result shown

⏺ Bash(python3 test_calc.py)                ← green bullet
  ⎿  ..
     ----------------------------------------------------------------------
     Ran 2 tests in 0.000s

     OK

⏺ Read(/abs/path/README.md)
  ⎿  Read 4 lines
                                              04:52 PM claude-haiku-4-5-20251001
⏺ Done.
────────────────────────────────────────────────────────────────────────────────────────────────────────  (dim rule)
  Showing detailed transcript · ctrl+o to toggle · ↑↓ scroll · v to open in vi · ? for shortcuts        verbose
```

- Every tool call is expanded into its own `⏺ Tool(args)` block with full output (no truncation).
- The input box is **replaced** by a dim rule plus a footer: hints on the left and the word
  `verbose` right-aligned. At 80 cols the footer drops `↑↓ scroll · v to open in vi`.
- ctrl+o again returns to the normal view. The turn-complete verb may be re-rolled on re-render.
- Background agent results are collapsed in the normal view with
  `⎿  Backgrounded agent (↓ to manage · ctrl+o to expand)`.

---

## 5. Permission prompts

All permission prompts share one component that **replaces the input and footer**. The transcript
above shows the pending tool header (`⏺ Update(calc.py)` or the running group with `⎿  $ cmd`).

### 5a. Edit file

```
⏺ Update(calc.py)

────────────────────────────────────────────────────────────────────────────────────────────────────────  (accent rule)
 Edit file                                                                                                (accent, bold)
 calc.py                                                                                                  (secondary)
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌  (dashed, low-contrast)
 1  def add(a, b):
 2 -    return a - b
 2 +    return a + b
 3
 4
 5  def mul(a, b):
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to make this edit to calc.py?                     ("calc.py" bold)
 ❯ 1. Yes                                                      (❯ + label accent; "1." secondary)
   2. Yes, allow all edits during this session (shift+tab)     ("(shift+tab)" bold)
   3. No

 Esc to cancel · Tab to amend                                  (secondary)
```

- Structure, top to bottom: a full-width accent rule, a bold accent title, a secondary subtitle
  (file path), a dashed `╌` rule, the diff (same renderer as 4b, starting at column 1), a dashed
  rule, the question, the numbered options, a blank line, and a hint line.
- Text column 1 (one space indent). Options at column 1: the `❯ ` cursor, then `N. ` (secondary),
  then the label. The selected label is accent. Others use the default foreground.
- A Write of a new file uses the title `Create file`, the question `Do you want to create notes.txt?`
  and a line-numbered preview (no markers, no bg) between the dashed rules.
- The question names the file in bold.

### 5b. Bash command

```
⏺ Running 1 shell command…
  ⎿  $ python3 test_calc.py

────────────────────────────────────────────────────────────────────────────────────────────────────────  (accent rule)
 Bash command                                                                                             (accent, bold)

   python3 test_calc.py                                        (primary; wraps hard at width with indent 3)
   Run the tests using unittest                                (secondary: model-provided description)

 This command requires approval

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don’t ask again for: python3 *                  (prefix rule suggested by the app)
   3. No

 Esc to cancel · Tab to amend · ctrl+e to explain
```

- The command preview is indented 3, followed by the description in secondary.
- Option 2 wording: `Yes, and don’t ask again for: <rule>`, where `<rule>` is a generated prefix
  pattern (`python *`, `python3 *`) or the exact command (`python3 -m unittest test_calc.py -v`).
  Note the typographic apostrophe in "don’t".
- **ctrl+e explain**: inserts an AI explanation between the command and "This command requires
  approval". The command and description dim to secondary, and the explanation is a paragraph, a
  blank line, then a risk line (`Low risk:` in the success color + text). The hint changes to
  `ctrl+e to hide`.

### 5c. Tab to amend

- Tab turns option 1 into an inline text field: `❯ 1. Yes, ` + a dim placeholder
  `and tell Claude what to do next`. Typing replaces the placeholder (`❯ 1. Yes, also add a
  docstring`). The hint line drops `Tab to amend` (`Esc to cancel`).
- Enter approves and sends the typed text as follow-up guidance.

### 5d. Results of a decision

- Approve: the prompt disappears, the tool header bullet turns success, and the result renders
  (4b).
- Reject with Esc on an Edit: `⏺ Update(calc.py)` / `  ⎿  User rejected update to calc.py`, and
  the proposed diff is still shown below. The turn ends (turn-complete line).
- Reject with "No" on a Bash inside a group: the group still collapses to `Ran 1 shell command`.
  Transcript mode shows a red bullet on `⏺ Bash(…)`.
- "Allow all edits during this session" (or shift+tab while the prompt is open) switches the
  session to **accept edits** mode. The footer turns into `⏵⏵ accept edits on`.

### 5e. Multiple pending prompts

Parallel tool calls that each need approval are shown **one at a time, sequentially**, with **no
counter** ("1 of 2"). The running group header shows `Running 2 shell commands…`. After the first
answer, the next prompt replaces it in place. A pinned task list stays visible *below* the prompt.

### 5f. Option keys

`↑/↓` move. `Enter` selects. **Number keys select immediately** (pressing `2` or `3` acts without
Enter). `Esc` cancels (= reject). `Tab` amends. `shift+tab` picks "allow all edits". `ctrl+e`
toggles the explanation.

---

## 6. Permission modes and plan mode

### 6a. Mode cycling (shift+tab)

Cycle order: `manual → accept edits → plan → auto → manual`. `auto` is skipped when the model
doesn't support it (Haiku: `manual → accept edits → plan → manual`). The footer changes instantly
(see 1c). No transcript entry is written.

### 6b. Plan mode working view

When in plan mode, a header strip appears above modal UIs:

```
────────────────────────────────────────────────────────────────────────────────────────────────────────  (secondary)
Planning: /Users/<user>/.claude/plans/plan-adding-a-sub-a-foamy-goblet.md                                (secondary)
────────────────────────────────────────────────────────────────────────────────────────────────────────
```

### 6c. Plan approval prompt

```
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔  (accent, upper-eighth block: full-screen pane top edge)

  ──────────────────────────────────────────────────────────────────────────────────────────────────── (plan-mode color)
   Ready to code?                                                                                       (accent, bold)

   Here is Claude's plan:
  ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
   Add sub(a, b) to calc.py                       ← plan markdown, rendered (H1 style etc.), scrollable
   …
                                                                                                     ↓   ← scroll indicator, right edge
  ────────────────────────────────────────────────────────────────────────────────────────────────────
   Claude has written up a plan and is ready to execute. Would you like to proceed?

   ❯ 1. Yes, and use auto mode
     2. Yes, manually approve edits
     3. Tell Claude what to change
        shift+tab to approve with this feedback      (secondary, under option 3)

   ctrl+g to edit in Vim · ~/.claude/plans/<slug>.md
```

- This is a **full-screen pane**. Its top edge is a row of `▔` in accent, and it covers the
  transcript. Inside, content is indented 2 to 3, framed by rules in the plan-mode color.
- The plan body is a scrollable viewport with a `↓` indicator at the right edge when more content
  is below.
- Option 1 depends on availability (`Yes, and use auto mode` vs. accept-edits wording). Option 3 is
  an inline text input.
- After approval the transcript shows `⏺ User approved Claude's plan` /
  `  ⎿  Plan saved to: ~/.claude/plans/<slug>.md · /plan to edit`, followed by the full plan text
  indented at column 5. The mode switches accordingly (to `manual` in our run with option 2).

---

## 7. Queue, interrupt, rewind

### 7a. Message queue (typing while busy)

```
⏺ The Terminal: A Window Into Computing
  … (streaming) …
  ❯ then a haiku                              ← queued message: user-message band, indented 2
  ❯ then a haiku

────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ Press up to edit queued messages            ← dim placeholder in the (empty) input
────────────────────────────────────────────────────────────────────────────────────────────────────────
```

- The input stays editable while the model works. Enter queues the message.
- Queued messages render at the bottom of the transcript, **indented 2**, with the user-message
  band style (`❯ ` + text on the band bg).
- The empty input shows the dim placeholder `Press up to edit queued messages`. `↑` pulls the
  queued messages back into the input.
- When the turn ends, the queue is sent as the next user turn. When Esc interrupts, the queued
  text is put back into the input box.

### 7b. Interrupt (Esc while working)

```
⏺ The Lighthouse Keeper's Secret
  … streamed text stops mid-sentence: "every peculiar way
  ⎿  Interrupted · What should Claude do instead?         (secondary)
```

- No turn-complete line follows. The input is focused and empty, or holds restored queued text.
- If the interruption happens before any output (for example during API retries), the user message
  is **removed** from the transcript and its text is restored into the input (`❯ hi`).

### 7c. Rewind menu (Esc Esc on empty input)

```
────────────────────────────────────────────────────────────────────────────────────────────────────────  (accent)
  Rewind                                                                                                  (accent, bold)

  Restore the code and/or conversation to the point before…

   ↑ 3 more above                                                    (secondary)

    Do these tool calls one at a time, sequentially, … 3)…           (default fg, truncated with …)
    No code changes                                                  (secondary)

  ❯ Use the Write tool to create notes.txt containing lines … pytho…   (selected: ❯ accent bold, text accent)
    notes.txt +31                                                    (file secondary, "+31" success color)


   ↓ 6 more below

  Enter to continue · Esc to cancel                                  (secondary italic)
```

- A list of previous user messages, 2 lines each (message truncated to one line, plus a change
  summary: `No code changes` or `<file> +N -M`). The last entry is `(current)` in italic accent. It
  is a windowed list with `↑ N more above` / `↓ N more below`.
- Enter opens a confirmation pane:

```
  Rewind

  Confirm you want to restore to the point before you sent this message:

  │ Use the Write tool to create notes.txt … python3 -c        ← quoted message, "│ " gutter, primary fg
  │ "for i in range(60): print(i)"
  │ (2m ago)                                                   ← secondary

  The conversation will be forked.
  The code will be restored -30 in notes.txt.                  ← "-30" in the removal color

  ❯ 1. Restore code and conversation
    2. Restore conversation
    3. Restore code
    4. Summarize from here
  ↓ 5. Summarize up to here                                    ← "↓" marks more options below

  ⚠ Rewinding does not affect files edited manually or via bash.   (secondary)
```

---

## 8. Tasks, questions, agents

### 8a. Task list

While the model is working, the task list hangs under the working line:

```
⟨spin⟩ ⟨verb⟩… (9s · ↓ 463 tokens)
  ⎿  ◻ Fix add bug
     ◻ Run tests
     ◻ Update README
```

When idle, it is **pinned above the input** (below the last turn):

```
  3 tasks (1 done, 1 in progress, 1 open)        ← secondary, numbers bold
  ◼ Fix add bug                                  ← in progress: ◼ in accent/⟨brand⟩ color, text bold
  ◻ Run tests                                    ← pending: ◻, default fg
  ✔ Update README                                ← done: ✔ success color, text secondary + strikethrough

────────────────────────────────────────────────────────────────────────────────────────────────────────
❯
```

- `ctrl+t` toggles the pinned list. It stays visible below modal prompts.
- The TodoWrite tool call itself is not shown as a separate tool block in the normal view.

### 8b. AskUserQuestion

```
 ☐ Test framework                                                    ← tab/chip: reverse (black on accent bg), full width

Should the test for sub(a, b) use unittest or pytest?                ← primary, bold

❯ 1. pytest                                                          ← selected: ❯ + label accent
     Use plain pytest-style test functions with assert statements.   ← description, secondary, indent 5
  2. unittest
     Use Python's built-in unittest module with a TestCase class.
  3. Type something.                                                 ← free-text option (secondary)
────────────────────────────────────────────────────────────────────────────────────────────────────────
  4. Chat about this                                                 ← separated escape hatch

Enter to select · ↑/↓ to navigate · Esc to cancel
```

- Replaces the input. With several questions, each gets a `☐ <header>` chip in the top row (it
  becomes checked when answered). Text starts at column 0, not 1.
- After answering, the transcript shows:
  `⏺ User answered Claude's questions:` /
  `  ⎿  · Should the test … use unittest or pytest? → unittest`.

### 8c. Subagents / background agents

```
⏺ Explore(Find calc.py and existing test patterns)
  ⎿  Backgrounded agent (↓ to manage · ctrl+o to expand)
…
⟨spin⟩ Waiting for 1 background agent to finish
…
⏺ Agent "Find calc.py and existing test patterns" finished · 23s
```

The agent panel sits **below the footer** (the last rows of the screen) while agents exist:

```
  ⏸ plan mode on (shift+tab to cycle) · ← for agents
  ⏺ main                                                                   ← bold (current)
  ◯ Explore  Find calc.py and existing test patterns            23s · ↓ 20.0k tokens
```

The `◯` uses the success color, the rest is secondary, and the stats are right-aligned. `←` focuses
this panel ("← for agents").

### 8d. Effort indicator

A right-aligned secondary `● high · /effort` appears in the hint row above the input, for models
that support effort levels.

---

## 9. Errors, retries, and informational commands

### 9a. Errors

- **Invalid model**: rendered as a normal assistant message with a
  primary-colored bullet: `⏺ There's an issue with the selected model (bogus-model-xyz). It may not
  exist or you may not have access to it. Run /model to pick a different model.` Then a
  turn-complete line (`for 0s`).
- **Retries**: the working line is **replaced in place** by
  `⟨spin⟩ Unable to connect to API (ConnectionRefused) · Retrying in 5s · attempt 4/10`. The error
  part (glyph + message) is in the error color, and `· Retrying in Ns · attempt N/10` is secondary.
  The countdown ticks every second. Esc during retries removes the turn and restores the prompt
  text into the input.

### 9b. `/context`

Output is attached to the command's user message via `⎿`:

```
❯ /context
  ⎿  Context Usage                                       (bold)
     ⛁ ⛁ ⛁ ⛀ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁   Haiku 4.5
     ⛁ ⛁ ⛁ ⛀ ⛁ ⛁ ⛁ ⛁ ⛁ ⛁   claude-haiku-4-5-20251001
     ⛁ ⛁ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   42k/200k tokens (21%)
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   Estimated usage by category       (italic)
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   ⛁ System prompt: 7.1k tokens (3.5%)
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   ⛁ System tools: 18.6k tokens (9.3%)
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   ⛁ MCP tools: 480 tokens (0.2%)
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   ⛁ Skills: 2k tokens (1.0%)
     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   ⛁ Messages: 14k tokens (7.0%)
                           ⛶ Free space: 157.9k (78.9%)

     MCP tools · /mcp (loaded on-demand)                  ("MCP tools" bold, rest secondary)
     └ 141 tools · 480 tokens
     …
     /context all to expand
```

A 10x10 grid where each cell is a category glyph in that category's color (system = gray, MCP =
cyan, skills = yellow, messages = purple, free = `⛶` gray). The legend is in the right column at
x+22, with the same glyph and color as its grid cells.

### 9c. Settings dialogs: `/status`, `/cost` (= Usage tab), `/config`, `/stats`, `/help`

```
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔  (accent ▔ top edge)
   Settings  Status   Config   Usage   Stats            ← "Settings" accent bold label; active tab = reverse (black on accent bg) " Status   "

   Version:          2.1.220                           ← keys bold, padded to a fixed 18-col key column
   Session name:     Review @te!ls
   cwd:              /very/long/path… (wraps with hanging indent at the value column)
   Model:            haiku (claude-haiku-4-5-20251001)
   MCP servers:      5 connected, 2 need auth, 1 disabled · /mcp   ← counts colored: success / warning / secondary
   …
   System diagnostics                                   (bold)
    ⚠ <diagnostic>                                      (⚠ warning color)

   Esc to cancel
```

- The dialog is a bottom **pane** whose top edge is a full-width row of `▔` in accent. It covers the
  lower part of the screen (transcript visible above). The input is hidden. Content is indented 3.
- Tabs: `←/→` (or Tab) switch. Active tab = black text on accent bg with 1 space padding.
- **Usage tab** (`/cost`, `/usage`): a "Session" section (cost, API/wall durations, code changes,
  per-model token usage: secondary text with aligned values), then rate-limit meters:

```
   Current session                                       (bold)
   ██████████████                                     28% used     ← 51-cell bar: fill fg accent on bg mid-gray
   Resets 6:50pm (Europe/Moscow)                         (secondary)
```

  followed by an insights section. The pane scrolls, with a `↓` at the right edge of the last row.
- **/help**: same pane, `Help` label + tabs `General · Commands · Custom commands`. The General tab
  repeats the 3-column shortcut grid (2b) and ends with a docs URL.

### 9d. `/model` picker

```
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Select model                                                         (accent bold)
   Switch between Claude models. Your pick becomes the default for new sessions. For other/previous model names,
   specify with --model.                                                (secondary)

     1. Default (recommended)  Opus 5 with 1M context · Best for everyday, complex tasks
     2. Opus (1M context)      Opus 5 with 1M context · Best for everyday, complex tasks
     3. Fable                  Fable 5 · Most capable for your hardest and longest-running tasks
     4. Sonnet                 Sonnet 5 · Efficient for routine tasks
   ❯ 5. Haiku ✔                Haiku 4.5 · Fastest for quick answers    ← current: label + ✔ in success color
     6. Fable 5.1 (disabled)   Update to 2.1.255+ to use Fable 5.1      ← disabled: whole row secondary

   ○ Effort not supported for Haiku                                     (low-contrast gray)

   Enter to set as default · s to use this session only · Esc to cancel
```

Two columns: the label is padded to 26 cells, then the description (secondary). The selection
cursor `❯` is accent. Choosing a model prints `⎿  Kept model as Haiku` (or similar) under the
`/model` command message.

---

## 10. Width, theme, and color-depth behavior

### 10a. 80x24

- All text re-wraps to the width minus the indent. The assistant message wraps at col 78 with a
  2-col indent. Rules are always full width.
- The welcome box keeps its 52-col left column and squeezes the right column, truncating each line
  with `…`.
- Slash menu: the name column shrinks to about 32 and descriptions wrap onto a second line
  (aligned to the description column) instead of truncating.
- Long unbreakable tokens (paths in Bash previews and tool headers) hard-wrap at the right edge,
  continuing at the block's indent (col 5 for `⎿` content, col 3 for permission previews, col 8 or
  2 for tool-header args in transcript mode).
- The transcript-mode footer drops the lower-priority hints at 80 cols (`↑↓ scroll`,
  `v to open in vi`).
- Resize triggers a full re-render of the transcript at the new width.

### 10b. Light vs dark

The terminal background is **not auto-detected**. The theme comes from settings (`theme: dark|light|…`
via /config). Light-theme role values observed (256-color):

| Role | Dark | Light |
|---|---|---|
| Secondary text | `#949494` | `#626262` |
| Input rules / subtle border | `#808080` | `#949494` |
| Interactive accent (selection, permission title, inline code) | `#afd7ff` | `#8787ff` |
| Accept-edits mode | `#afafff` | `#af00ff` |
| Plan mode | `#5fafaf` | `#008787` |
| Warning | `#ffd700` | `#af875f` |
| Diff removed bg / added bg | `#5f0000` / `#005f00` | `#ffd7d7` / `#d7ffd7` |

### 10c. 16 colors (`TERM=xterm`)

The UI chrome is quantized to basic ANSI SGR (30–37 and 90–97 only): accent → bright blue,
secondary → black/white (theme-dependent), brand → red, borders → white. Bold, dim and italic stay.
The **diff renderer still emits 256-color** (`38;5;…`/`48;5;…`) regardless.

### 10d. `NO_COLOR=1`

- **All SGR is dropped from the chrome**: no colors, *and* no bold, dim, italic or reverse. The
  user-message band disappears (plain `❯ text`). Selected rows in menus can't be told apart. Only
  the `❯` cursor marks the selection in option lists, and nothing marks it in the `/` menu.
- The **diff renderer ignores NO_COLOR** and still prints colored backgrounds.
- Recommendation for us: under NO_COLOR keep the *structural* cues (bold/reverse for selection, a
  `❯` cursor in every list, `+/-` markers) and drop only color. Apply the same policy to diffs.

---

## 11. Styling roles (consolidated)

Values in parentheses are the dark-theme observations, for lightness and contrast reference only.
Brand-hued roles are **OWN DESIGN**.

| Element | Attribute (role) |
|---|---|
| Primary text, assistant prose | default fg (white) |
| Secondary text (hints, footers, summaries, descriptions, turn-complete line, tips) | gray fg (`#949494`) |
| Low-contrast decoration (dashed `╌` rules, `❯` on user band, disabled notes) | dark gray fg (`#4e4e4e`) |
| Input top/bottom rules | subtle gray fg (`#808080`) |
| Input rules with a named session | session color fg; label = reverse (black on session color) |
| User message | full-row bg band (`#3a3a3a`), text primary, `❯` low-contrast |
| Shell (`!`) message | full-row bg band (`#5f5f5f`), `!` in shell color |
| Shell mode input (rules, prompt, footer) | shell color (pink `#ff87af`) |
| Assistant bullet `⏺` | primary fg |
| Tool bullet success / error / running | green (`#87d787`) / pink-red (`#ff87af`) / blinking secondary |
| Tool name in header | bold |
| Tool summary counts ("Read **2** files", "Added **1** line") | bold (same color as surrounding text) |
| Collapsed tool group line | secondary, counts bold |
| `⎿` connector and result text | secondary |
| Truncation `… +N lines` | secondary |
| Inline code, file refs, @mentions, slash command names in text | accent (`#afd7ff`) |
| H1 | bold + italic + underline |
| H2, H3 | bold |
| Code blocks in messages | ANSI-16 syntax colors, no bg, no fence |
| Blockquote | `▎` gutter + italic |
| Tables | box-drawing, default fg |
| Working-line spinner + status word | OWN DESIGN (brand color + shimmer in Claude Code) |
| Working-line stats `(12s · ↓ 1.1k tokens · thinking)` | secondary; `thinking` pulses in gray shades |
| Retry/error line | error color for the message, secondary for the countdown |
| Warning notices (`⚠ …`) | warning color (`#ffd700`) |
| Permission/dialog title | accent + bold; top rule accent |
| Permission subtitle (file path) | secondary |
| Option list: cursor `❯` + selected label | accent |
| Option list: `N.` numbers | secondary |
| Emphasized words in questions/options (file name, `(shift+tab)`) | bold |
| Hint line under prompts | secondary (italic in some dialogs) |
| Popup menu selected row | accent fg on whole row (no reverse) |
| Popup menu unselected row | secondary |
| Fuzzy-match characters | accent |
| Mode: accept edits / plan / auto | lavender / teal / yellow fg on the mode text only |
| Pane top edge (`▔` row) | accent |
| Active tab | reverse: black fg on accent bg, bold |
| Meter bar | fill accent on mid-gray bg (`#878787`), 51 cells |
| Task in progress | `◼` accent/brand + bold text |
| Task done | `✔` success + secondary strikethrough text |
| Task pending | `◻` default |
| Diff add / remove | see 4c (green / red bg, brighter word-diff bg, colored markers) |
| Diff context line numbers | dim |
| Ghost prompt suggestion, input placeholders | dim |
| Right-aligned contextual hint row above input | secondary |
| Risk label in explanation ("Low risk:") | success color |
| Rewind change counts `+N` / `-N` | success / removal color |

---

## 12. Key bindings

| Context | Key | Action |
|---|---|---|
| Input | Enter | Submit (or queue if busy) |
| Input | `\` + Enter, Alt/Opt+Enter | Insert newline |
| Input (empty) | `?` | Toggle shortcut help in footer |
| Input (first char) | `/` | Open command menu |
| Input | `@` | Open file/resource mention menu |
| Input (first char) | `!` | Shell mode |
| Input | Esc (with text) | 1st: hint "Esc again to clear"; 2nd: clear input |
| Input (empty, idle) | Esc Esc | Open rewind menu |
| Input | ctrl+u | Delete input; hint "Ctrl+Y to paste deleted text" |
| Input | ctrl+y | Paste deleted text (from on-screen hint) |
| Input | ctrl+g | Edit prompt in $EDITOR |
| Input | ctrl+s | Stash prompt |
| Input | ctrl+shift+_ | Undo |
| Input | ctrl+v | Paste image |
| Input | Tab on ghost suggestion | Accept suggested prompt into input |
| Input (busy, queue non-empty) | ↑ | Edit queued messages |
| Global | shift+tab | Cycle permission mode |
| Global | ctrl+o | Toggle transcript (verbose) mode |
| Global | ctrl+t | Toggle task list |
| Global | opt+p | Switch model |
| Global | ← (empty input) | Focus agent panel (from footer hint, not exercised) |
| Global | ctrl+z | Suspend |
| Busy | Esc | Interrupt turn (queued text returns to input) |
| Menu (`/`, `@`) | ↑/↓, Tab, Enter, Esc | Move, complete, run/insert, close |
| Option prompts | ↑/↓, 1–9, Enter, Esc | Move, pick directly, confirm, cancel |
| Permission prompt | Tab | Amend (inline feedback on "Yes") |
| Permission prompt | shift+tab | "Allow all edits this session" |
| Bash permission | ctrl+e | Show/hide AI explanation |
| Plan approval | shift+tab | Approve with typed feedback |
| Plan approval | ctrl+g | Open plan file in editor |
| Transcript mode | ↑/↓, v, ctrl+o | Scroll, open in vi, exit |
| Dialog panes | ←/→ or Tab, Esc | Switch tab, close |
| /model | Enter, s, Esc | Set default, this session only, cancel |

---

## 13. State transitions

```
            ┌──────────── shift+tab cycles mode (footer only) ────────────┐
            v                                                             │
  [Idle: input focused] ──Enter──> [Busy: working line] ──stream──> [Busy: text/tools]
     ^   │  │  │                     │   │                               │
     │   │  │  └─ "/" "@" ─> [Menu above input] ─Esc/Enter─┐             │ tool needs approval
     │   │  └──── "!" ─────> [Shell-mode input] ─Enter─> runs → model turn
     │   └─ Esc Esc (empty) ─> [Rewind pane] ─Enter─> [Rewind confirm] ─> fork/restore
     │                                   │   │                           v
     │                       Enter=queue │   │ Esc               [Permission prompt replaces input]
     │                                   v   v                     │ approve │ reject/Esc
     │                    [Queued rows]  [Interrupted · What should…]   v         v
     │                                                       continue turn   "User rejected…", turn ends
     │                                                                │
     └──────── turn ends: "⟨done⟩ … for Ns" + ghost suggestion ◄──────┘

  Plan mode turn ──ExitPlanMode──> [Full-screen plan approval pane] ──1/2──> mode switches, execution
                                                                    └─3──> feedback → re-plan
  AskUserQuestion ──> [Question UI replaces input] ──Enter──> "User answered…" → turn continues
  API failure ──> working line becomes "Unable to connect… · Retrying in Ns · attempt k/10"
                  ──Esc──> turn removed, prompt text restored into input
```

Notes:

- Only one modal can occupy the input slot at a time. Multiple permission requests are serialized.
- Pinned elements (task list, agent panel) survive across modals.
- Transcript mode (ctrl+o) is an overlay view of the same transcript. Leaving it restores the
  normal view, with no state change.
- Live tool groups mutate in place (the counts increase), then freeze to past tense when the
  group ends.

---

## 14. Identity elements to design ourselves (OWN DESIGN)

1. **Spinner**: 1 cell at column 0 of the working line, its animation frames, and its color.
2. **Status words**: the animated word after the spinner while working (and any shimmer
   effect), plus the past-tense word on the turn-complete line (`⟨done⟩ ⟨past-verb⟩ for 15s`).
   Keep the slot, the `…` and the stats group. Choose our own vocabulary (or a plain fixed word).
3. **Brand/accent hue**: Claude Code uses it for the welcome box border and title, spinner,
   in-progress task glyph and model-name highlights. Choose our own.
4. **Interactive accent** (selection, dialog titles, inline code): pick our own hue, keeping a
   light/dark pair with similar contrast.
5. **Logo / mascot art** in the welcome box's left column, the **greeting copy**, and the **app
   title inset** in the box border.
6. **Welcome right-column content** (our own tips / what's new sources).
7. **Tip copy** under the working line, and the **ghost-suggestion** strategy.
8. **Glyph set choices** where we want our own look: the `⏺` bullet, the `⎿` connector, mode glyphs
   (`⏸`, `⏵⏵`), task glyphs, and context-grid glyphs. The structure is reusable. The specific glyphs
   are optional to copy, but don't copy the spinner frames.

---

## 15. Not captured / caveats

- **Per-tool collapsed blocks with `… +N lines (ctrl+o to expand)` for Bash/Read/Grep/Glob**: in
  v2.1.220 consecutive read/search/shell calls are always merged into a single group summary
  ("Searched for 1 pattern, read 1 file, … ran 2 shell commands"). The per-tool form only appears in
  transcript mode, where nothing is truncated. The only truncation marker seen in the normal view
  is `… +20 lines` under a Write result.
- **Dedicated Grep/Glob tool headers**: the models used Bash (`grep`, `ls`) instead, so the
  dedicated tool display names weren't observed in transcript mode.
- **`#` memory shortcut**: not present in this version.
- **Esc-to-interrupt hint text**: not present in this version (Esc works silently).
- **Rejection detail text for a Bash "No"** in transcript mode: only the red bullet was shown.
- **Background-color auto-detection**: none. Light/dark is a setting.
- **Rate-limit / overloaded (529) errors**: not triggered. Only connection-refused retries and an
  invalid-model error were captured.
- The pseudo-terminal emulator didn't keep scrollback. Content that scrolled off was recovered
  by temporarily resizing the terminal taller (the app re-renders the full transcript on resize) or
  by replaying the raw byte stream.
