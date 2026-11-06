Makes several replacements in one file as a single change: either all of them land or the file stays as it was. Use it instead of a series of Edit calls when one change touches several places of a file.

Each edit follows the rules of Edit: read the file first, copy the text exactly without line-number prefixes, and `old_string` must match once unless `replace_all` is set. The edits run in order and each sees the result of the ones before it, so a later one must not look for text an earlier one replaced. If any edit fails, nothing is written and the error names it. It does not create files.
