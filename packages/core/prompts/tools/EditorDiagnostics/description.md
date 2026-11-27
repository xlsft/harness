Lists what the user's editor reports in its problems panel: compiler errors, linter warnings and anything else its extensions find, for the session's folders. It sees problems no command in this session would show, including those in files with unsaved changes.

Only offered while an editor is attached to the session. Use it after a change to check what the editor thinks of it, or when the user mentions errors they can see.

Without `severity`, errors, warnings and information are listed, not hints. Give `path` to ask about one file. A language server may still be working right after an edit, so an empty answer straight away doesn't prove the file is clean.
