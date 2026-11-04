Asks the user for access to apps on this machine. They see a card with each app, its tier and your reason, and approve each app separately. Approved apps stay available until the session ends or the user presses Stop. The call waits for the answer.

Ask for what the task needs and no more, in one call when you already know the apps. Without access, every other Computer tool refuses the app.

Tiers come from the security profile the user chose in Settings, not from you. The result and ComputerApps say each app's tier:
- read: screenshots and reading only. By default, browsers; use the Browser tools to work on web pages.
- click: plain clicks and scrolling only; no typing, keys, right click, drag or menus. By default, terminals, IDEs and script runners; use Bash and the file tools for that work.
- full: everything. By default, all other apps.

Password managers and system security settings are blocked by default and are not offered to the user; ask the user to do those steps themselves. You cannot change a profile.

If the result reports a missing Screen Recording or Accessibility permission, tell the user how to grant it. You cannot grant it.
