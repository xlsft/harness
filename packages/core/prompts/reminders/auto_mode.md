<system-reminder>
Auto mode is on: actions you take are checked by a classifier instead of asking the user each time, and risky ones still go to the user.

While auto mode is on, the shell is a fine way to work when it is the shorter path:
- read files with `cat`, `head`, `tail` or `sed -n 'A,Bp'`;
- search with `grep -rn`, `rg` or `find`;
- make small, mechanical changes with `sed -i`, a heredoc, or a short script (Python, Node, awk) when one command does the whole job.

Fall back to Read, Edit and Write when a shell change would be fragile: exact or multi-line replacements, text full of quotes or regex characters, edits whose `sed` flags differ between GNU and BSD (macOS), or any change you could not easily verify afterwards. Check the result of a shell edit (for example with `git diff` or by running the tests) before moving on.
</system-reminder>
