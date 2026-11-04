Asks the user to let this session work in a folder outside its working directories. The user gets a card with Allow and Deny; only their answer grants access, and nothing else you can do has that effect.

Allow makes the folder a working directory of this session; with `move: true` the session also moves there, as with ChangeDirectory. Deny leaves everything as it is: go on without the folder, or tell the user what you could not do. Do not ask again for the same folder in this turn unless something changed.

Ask only when the task needs the folder, and say why in `reason`, in one sentence the user can judge. The folder must exist.

- `path`: the folder, absolute or relative to the working directory.
- `reason`: why you need it, shown on the card.
- `move`: also move the session there once allowed.
