Opens a new tab in the user's Terminal pane and starts their shell in it, without running anything. The pane opens by itself and the result names the tab. Use it when the user asks for a terminal in some folder, or to prepare a tab for them to work in. To run a command right away, call TerminalRun instead, which opens a tab by itself.

`cwd` must be an absolute path inside the working directories; by default the tab starts in your current directory. Needs no permission.
