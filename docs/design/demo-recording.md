# Recording a demo into a skill, and the iOS Simulator pane

Two features that share the macOS computer use helper (`packages/client/computeruse/helper/main.swift`): the user shows
a workflow once and the agent turns it into a `SKILL.md`, and a pane that streams a booted iOS Simulator with tools for
the agent to drive it. Everything here is device-local: recordings, screenshots and simulator frames stay on this
machine, and nothing is read from `.claude/` or `CLAUDE.md`.

## Recording a demo

### Flow

1. The user starts a recording from the session (the composer's tools menu, "Record a demo"), picking the apps it may
   watch. Only a frontend on this machine can start one, for a live session of this machine
   (`StartDemoRecording`); the relay has no command for it.
2. The client starts the helper in `--record` mode. It shows a red panel at the top of the screen ("Recording a demo")
   with a Stop button; `⌃⌥⌘.` stops it too, and so does the Stop of the session's recording bar. The session publishes
   `DemoRecordingState` while it runs.
3. The helper reports clicks, scrolls, key presses and app switches as JSON lines. The client turns them into steps
   (`computeruse/recording.go`): consecutive plain keys in one app become one "typed" step, combinations and Return are
   key steps, a click names the accessibility element under the pointer (role, title, description). About half a second
   after a step the client captures the window it happened in, blanks the secure fields in it, and stores the JPEG in
   `HARNESS_HOME/demo-recordings/<id>/`.
4. Stop ends the helper. The client writes `recording.json` next to the screenshots and gives the session's model a
   reminder (`demo_recorded`) that names the recording. The agent reads it with `DemoRecording` (steps as text,
   screenshots of the steps it asks for), drafts the skill, and calls `DemoSkillSave`.
5. `DemoSkillSave` shows a review card with the draft, editable. Nothing is written until the user saves it; the card
   says where it goes (`.harness/skills/<name>/SKILL.md` of the project, or `~/.harness/skills/<name>/SKILL.md`). The
   tool returns the path, or that the user discarded it, with their note.

### Decisions

- **Same switch, same profile as computer use.** Recording needs computer use turned on (Settings > Computer use) and
  the same OS permissions (Accessibility for the event tap and the element lookups, Screen Recording for the
  screenshots). It never prompts for a permission; it reports what is missing.
- **Per-app approval.** The start card lists the running apps with the tier the active security profile gives them. The
  user ticks the apps to record; apps the profile blocks (Harness itself, password managers, the system's security
  apps) cannot be ticked. Events in any other app are dropped (a step "Used another app (not recorded)" keeps the
  order readable) and its windows are never captured. A step in an app whose tier is lower than what the step needs
  (typing into a click-only terminal, clicking in a read-only browser) is recorded with the tier, and the reminder tells
  the agent to write that part of the skill for the tool that may do it (Bash, the Browser tools) instead of computer
  use. The recording grants nothing: a skill that later uses computer use asks for access like any other session.
- **Password fields.** The helper drops keystrokes while macOS secure input is on or a secure field has the focus, and
  sends only `secure_key`: the step says "Typed into a password field (not recorded)". macOS also keeps those events
  from event taps by itself. Before a screenshot is stored, the client asks the helper for the frames of the secure
  fields of that window (`secure_fields`, plus the focused element while secure input is on) and paints them black.
  A click on a password field is kept as a click, without any value.
- **Screenshots stay on the device.** They live under `HARNESS_HOME/demo-recordings` (0700/0600), are never synced or
  uploaded, and the last 10 recordings are kept. `DemoRecording` results that carry images are marked like computer
  use results (`"computer_use": true`), so the transcript upload and the relay mirror leave the images out; the model
  sees them as it sees computer use screenshots. A discarded recording is deleted with its screenshots.
- **The agent drafts, the user saves.** The model gets text first and screenshots only of steps it asks for (6 at a
  time). The save goes through a card and not through `Write`, so the user can edit the draft and sees the final text;
  the card is the approval, so the permission engine does not ask a second time (as with `ComputerAccess`). A name
  that is not a valid skill name, or one that exists, is refused before the card.
- **Hotkey.** `⌃⌥⌘.`: three modifiers, so it does not collide with app shortcuts, and the key is not recorded.
- **Not the agent's tool.** The model cannot start or stop a recording; only the user can, from a frontend on this
  machine or the panel.

## iOS Simulator pane

### What it does

- The Simulator pane lists the simulators of this Mac (`xcrun simctl list -j devices`), boots and shuts one down, and
  streams the booted one: the client polls `xcrun simctl io <udid> screenshot --type=jpeg -` (about 4 frames a second,
  unchanged frames skipped) over a server stream (`WatchSimulator`). A click on the frame is a tap, a drag a swipe,
  keys typed while the frame has focus go to the device, and Home is a button.
- The agent's tools: `SimulatorList`, `SimulatorBoot`, `SimulatorInstall`, `SimulatorLaunch`, `SimulatorScreenshot`,
  `SimulatorTap`, `SimulatorSwipe`, `SimulatorType`, `SimulatorButton`. They go through the permission engine:
  `SimulatorList` and `SimulatorScreenshot` are read-only, the others ask unless a rule allows them (a rule
  `Simulator*` covers them all).

### Decisions

- **Input through SimulatorKit, not through the Simulator window.** `simctl` has no input command. Clicking the
  Simulator.app window with the computer use driver would take the user's mouse, need that window visible and in front,
  and need Accessibility. Instead the helper loads Xcode's `CoreSimulator` and `SimulatorKit` at run time and sends
  Indigo HID messages through `SimDeviceLegacyHIDClient`, the path Simulator.app uses: headless, no macOS permission,
  Simulator.app need not be open. The messages come from SimulatorKit's own exported builders
  (`IndigoHIDMessageForMouseNSEvent`, `IndigoHIDMessageForKeyboardArbitrary`, `IndigoHIDMessageForButton`), called
  with the arguments Simulator.app passes in Xcode 26 (read from its disassembly): a point as a fraction of the screen
  with a 1×1 size, no second finger, the main screen's digitizer target `0x32`; buttons to the hardware target
  `0x33`. These are private APIs: a missing symbol or class makes the input refuse with a clear error, and the frame
  stream and the `simctl` tools keep working.
- **No new dependencies.** No idb, no WebDriverAgent, no extra binaries: `xcrun simctl` and the helper the app already
  ships.
- **Typing.** ASCII goes as HID key presses (with Shift where needed); anything else is put on the device's pasteboard
  with `simctl pbcopy` and pasted with ⌘V. There is no secure field check inside the simulator: iOS does not expose its
  accessibility tree to the Mac this way. The prompt says not to type real credentials.
- **Coordinates.** The tools take pixels of the last `SimulatorScreenshot` of that device, like computer use; the pane
  sends fractions of the frame.
- **Which devices.** Any simulator of this Mac can be listed; the tools act on one the user or the agent booted. The
  tests create a throwaway device, boot it, and delete it; they skip without Xcode or without an iOS runtime.
- **Device-local.** Frames and screenshots stay on the machine (the screenshot results carry the `computer_use`
  marker); the pane is not offered for a session of another machine.
