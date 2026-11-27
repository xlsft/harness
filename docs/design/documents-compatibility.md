# Documents: what a person must check by hand

The tests of xlsft\`s harness read files back with its own parsers, and the checks read them against the format's rules. Neither proves that Office opens a file without a repair prompt. LibreOffice opened the files we create in one manual run (see below); Word, Excel and PowerPoint have not been tried, because no copy of Office is available to the tests.

Go through this list with real Word, Excel and PowerPoint before relying on an operation, and again after changing `packages/client/documents`. For each one: the file opens with no repair prompt, it looks right, and it saves again.

To get files to try, generate them with `DocumentEdit` in a session, or run the fixture writer:

```bash
cd packages/client
HARNESS_WRITE_EVAL_FIXTURES=/tmp/doc-fixtures go test ./documents -run TestWriteEvalFixtures
```

## Word

- [ ] A document from `create` with a heading, bullets, numbers, a table and a page break.
- [ ] `replace_text` across runs of different formatting: the new text has the first run's format, and the text after it keeps its own.
- [ ] A tracked `replace_text`, `insert_after` and `delete_paragraph`: all appear in the review pane under the given author, and Accept All and Reject All both give the expected text.
- [ ] `accept_change` and `reject_change` on files that have insertions and deletions.
- [ ] `add_comment` on a file with no comments part, with and without an anchor, and `delete_comment`.
- [ ] An edit in a header, a footer, a footnote and a text box. A text box edited here keeps its old text in the `mc:Fallback` copy, and the report says so: check that Word shows the new text.
- [ ] A file with a table of contents, after a heading changed: it is flagged, and Update Field brings it up to date.
- [ ] `insert_image` inline, after `find` in a paragraph, floating with `x` and `y`, and in a header: the picture shows at the size reported, with its alt text in Word's Alt Text pane.
- [ ] `replace_image` on a picture Word inserted (it has an SVG copy in its extension list for some pictures): the new picture shows, not the old one; and tracked, with Accept and Reject.
- [ ] `reply_comment`, `resolve_comment`, `reopen_comment` on a file with and without `commentsExtended.xml`, and on one from current Word with `commentsIds.xml`: the thread and the resolved state show in the comments pane.
- [ ] `insert_after` with a row target, `delete_row`, `move_row`, tracked and not; Accept All and Reject All on the tracked rows.
- [ ] `.dotx` template used through `create` with `template`: the result opens as a document, not a template.
- [ ] `.docm`: macros are still there after an edit.

## Excel

- [ ] A workbook from `create` with formulas, a date cell and a bold header row.
- [ ] `set_cell` on an input with dependents: opening recalculates (`fullCalcOnLoad`), and the values match what the report said.
- [ ] Replacing a formula with a value: `calcChain.xml` was removed, and Excel rebuilds it with no prompt.
- [ ] New cells and rows past the old used range, in a row that had a `spans` attribute.
- [ ] `type: date` on a cell with a plain style: a new cell format was added, and the date shows as a date.
- [ ] An inline string in a cell: it shows, sorts and is converted to a shared string when saved.
- [ ] Writing to a child of a shared formula, then recalculating: the other cells are unaffected.
- [ ] External links, pivot tables, charts, conditional formats and data validation survive an edit next to them. Data validation rules are not enforced on written values: note what Excel does with a value that breaks one.
- [ ] `.xlsm`: macros are still there after an edit.

## PowerPoint

- [ ] A deck from `create` with each layout (title, content, section, blank) and speaker notes.
- [ ] `set_text` and `replace_text` in a placeholder: the theme's format still applies.
- [ ] `set_geometry` on a placeholder that inherited its position, and on one that has its own.
- [ ] `add_textbox`, `delete_shape`.
- [ ] `add_image` and `replace_image` (PNG, JPEG, GIF), alt text in the Alt Text pane, and a replaced picture that a cloned slide shares.
- [ ] `add_slide` from a layout, in a deck with and without sections.
- [ ] `add_slide` with `clone_of` on a slide with a chart: the clone's chart is its own, and editing its data leaves the original alone. Also on slides with SmartArt, video, comments and animation, none of which is in the fixtures.
- [ ] `delete_slide` on a slide with notes, and one whose image another slide shares.
- [ ] `move_slide` across sections: the slide lands in the section of its new neighbour.
- [ ] Overflow reports against what PowerPoint shows, for fonts on this machine; line breaks may differ slightly.

## PDF

- [ ] `rotate`, `delete_pages`, `merge` and `split` output in Acrobat and Preview, with an outline and with annotations in the source.
- [ ] `fill_form` on a real form: the values show in Acrobat, and the appearance matches the field (fonts, checkboxes, radio buttons, combo boxes). Fixtures only cover text fields.
- [ ] A signed file and an XFA form: both are reported and refused. The fixtures declare them in the AcroForm; try real ones.
- [ ] A PDF from `create`: only Latin-1 text, Helvetica, plain layout.
- [ ] `add_text` and `add_image` on a page from Word, one from a scanner and a rotated one, in Acrobat and Preview: placed where asked, nothing of the page lost.

## The engine

- [ ] One run per release against the real LibreOffice, with the sandbox off: `HARNESS_TEST_LIBREOFFICE=1 HARNESS_TEST_PDFIUM=1 go test ./documents -run Real`. It is off by default because a failed launch leaves crash reports behind. It last passed on macOS, LibreOffice from Homebrew's cask, in November 2026.
- [ ] `render` with `run_outside_sandbox` on macOS with the sandbox on: the prompt appears, and declining it runs nothing.
- [ ] `convert` docx→pdf, pptx→pdf, docx→odt and odt→docx with the real LibreOffice, and with `run_outside_sandbox` on macOS: the prompt names the command and output folder.
- [ ] Linux and Windows have never run the real engine. The sandbox has no driver there yet.
