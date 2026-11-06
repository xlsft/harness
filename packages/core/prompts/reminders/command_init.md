The user wants an instructions file for the repository at {{cwd}}, so that an agent starting cold there can be useful right away. An agent reads this file at the start of every session, so every line costs something on every task.

The file to write is `{{target}}`. Already there: {{existing}}. If one is there, read it and improve it: keep what's still true and what its authors clearly care about, fix what's wrong, and add what's missing.

What the user asked to emphasize, if anything: {{arguments}}

Find out before you write:
- how to build, run, lint and test, read from the manifests, task runners and CI config, not guessed; include how to run one test;
- how the code is organized, where it isn't obvious from the folder names: what talks to what and where the boundaries are;
- rules the code follows that a newcomer would break: generated files, conventions the linter doesn't catch, things never to do;
- what the README, contributing guide and any other agents' rule files already say.

Then write it:
- Include what can't be found quickly by looking, and leave out what can. No file listings, no general advice about good code, nothing that would be true of any project.
- Short and concrete: commands as they are typed, paths as they are. Aim for a page; a large repository may need a little more.
- Don't invent anything. If you couldn't verify something, leave it out, or run it to find out if that's cheap and safe.

When you're done, tell the user in a few lines what you wrote and anything you couldn't determine. Don't commit it.
