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

## Frontend

- **Where it starts.** "Record a demo" is in the session's tools menu, only for a session of this machine. It opens a
  picker above the composer (not a modal) with the apps from `ListDemoRecordingApps`, their tiers and notes; blocked
  apps are shown but cannot be ticked, and the client's reason (computer use off, a permission missing, not a Mac)
  replaces the list. While recording, the same place is the red bar: steps so far, the last step, the hotkey, Discard
  (deleted, the agent is not told) and Stop (kept, handed to the agent).
- **The review card replaces the tool row** of `DemoSkillSave`, as the access card replaces `ComputerAccess`. It has the
  SKILL.md in a plain monospace textarea, the scope (only the scopes the session has a folder for) with the target
  path, and a note for the agent. A frontend that is not on the machine sees the card but cannot answer it.
- **The iOS Simulator pane** is a right pane (`simulator`) offered in the tools menu for a session of this machine. The
  frame stream is blob URLs, the previous one revoked as the next arrives; points are fractions of the image box, a
  press shorter than 6 px of movement is a tap (a long press past 500 ms holds), anything else a swipe with the drag's
  duration. Characters typed into the focused frame are batched for 150 ms into one text input; named keys and
  combinations go as keys. The agent's last action on the shown device is a small note over the frame for 4 seconds.
- **Reminder fallback.** The client has a fallback text for `demo_recorded`, as for other reminders, so a session whose
  prompt bundle predates it still gets a real message when the recording wakes it.

## What was tested, and how

- Recording: unit tests with a scripted feed in `computeruse/recording_test.go` (steps, secure keys dropped, secure
  fields blanked in the stored JPEG, other apps dropped, tiers), and `server/demo_test.go`, which goes from the start
  RPC through the reminder, `DemoRecording`, `DemoSkillSave` and the review card to the user's edited file on disk.
  The helper's `--record` mode, the event tap, the on-screen panel and the hotkey have not been run against real apps.
- Simulator: `simulator/simulator_test.go` with a fake `simctl` and fake input; `simulator/live_darwin_test.go` creates
  its own throwaway device, boots it, drives it and deletes it, and skips without Xcode or without an iOS runtime (on
  the development machine it skipped: no iOS runtime installed). The helper's `sim_*` operations type-check
  (`swiftc -typecheck`) but have not run against a booted device.
- UI: reducer and label tests (`packages/ui/test/demoRecording.test.ts`) and a look through the mock transport.

## Manual checklist (needs the owner, on a Mac)

Recording a demo uses the owner's own screen and apps, so it is left for the owner to try. Use apps with nothing private
in them (a new note in Notes, TextEdit) and a throwaway test account where a sign-in is involved.

1. Settings > Computer use: turn it on; grant Accessibility and Screen Recording to the app; pick the Strict profile.
2. In a local session, tools menu > Record a demo. Check: Harness is not listed; 1Password and System Settings are
   listed as Blocked and cannot be ticked; Safari shows Read only and Terminal Click only.
3. Tick Notes and TextEdit, Start recording. Check the red panel at the top of the screen and the red bar above the
   composer, with the step count going up.
4. In Notes: New Note, type a title, ⌘S or Return. Switch to Finder and click something (should become "Used another
   app (not recorded)"). Switch back to TextEdit and type a line.
5. Password check: open a page or app with a password field inside a recorded app (or TextEdit's nothing: use a test
   sign-in sheet in a recorded app), click into it and type a fake password. The step must read "Typed into a
   password field (not recorded)", and its screenshot must show the field black.
6. Stop with ⌃⌥⌘. (hotkey); start another and stop it with the panel's Stop; start a third and stop it with Discard in
   the bar. The first two wake the agent with the `demo_recorded` reminder; the discarded one does not, and its folder
   under `HARNESS_HOME/computer-use/demo-recordings/` is gone.
7. Let the agent read the recording and propose a skill. In the review card, edit a line, choose "My skills" or "This
   project", save. Check the file has your edit, and that saving the same name again is refused.
8. Check nothing left the machine: the session on another device (or the server's transcript) shows no recording
   screenshots, and no Demo* or Simulator events appear through the relay.
9. iOS Simulator (needs Xcode with an iOS runtime): open the pane, boot a device, tap an icon, swipe between home
   screens, type into Safari's address bar on the device, press Home. Ask the agent to build a small app for the
   simulator and run it with SimulatorInstall, SimulatorLaunch, SimulatorScreenshot and SimulatorTap; each action asks
   unless a `Simulator*` rule allows it, and the pane shows what the agent did.
