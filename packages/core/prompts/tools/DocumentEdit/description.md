Changes or creates Word, Excel and PowerPoint files and PDFs. Use this, not Bash or Python: only what changes is rewritten, so everything else in the file keeps its exact bytes. An edit is verified on a temporary copy and refused if it would break the file; the report gives new problems, inherited ones and what could not be verified. Passing does not prove Office opens the file. The result also gives the new addresses of what changed.

`operation`:
- `edit` (the default): apply `edits`, a batch that succeeds or fails whole. Read the file with DocumentRead first. Targets are addresses with their `fingerprint`; a mismatch or more than one match is refused with the current state or the candidates.
- `create`: a new file from `spec`, or from `template` (a file already read) plus `edits`. Load the `documents` skill first for anything that needs design judgement.
- `merge` (PDF): `sources` into `file_path`. `split` (PDF): `pages` of `source` into `file_path`, one output per call. Sources must have been read.

`edits[].op` by format:
- Word: `replace_text` (`find`, `text`; `target` narrows it, `all` allows several), `set_text`, `insert_after`, `insert_before` (`text`, optional `style`), `delete_paragraph`, `add_comment` (`text`, optional `find` as anchor), `delete_comment` (`comment:3`), `accept_change`, `reject_change` (`change:7` or `all`). New text takes the format of the first run the match touches; the report says when the runs differ. Edits are tracked when the document has tracking on; `track` overrides it, `author` names who.
- Excel: `set_cell` (`target` `Sheet1!B2`; `value` as a JSON number, string or boolean, or `formula`; `type` `date` with an ISO date, `string`, `number`), `clear_cell`. An empty cell needs no fingerprint. The report gives before and after values of dependent cells. Rows and columns cannot be inserted. Shared formula masters, array formulas, merged non-anchor cells, table headers and protected sheets are refused.
- PowerPoint: `set_text`, `replace_text`, `set_geometry` (`x`, `y`, `cx`, `cy` in EMU), `delete_shape`, `add_textbox` (`target` a slide), `add_slide` (`layout`, `title`, `text`, `position`; or `clone_of`), `delete_slide`, `move_slide` (`position`).
- PDF: `rotate` (`degrees`, `pages`), `delete_pages` (`pages`), `fill_form` (`fields` by full name). Signed, encrypted and XFA files are refused.

`spec`: Word and PDF `{title, blocks: [{type: heading|paragraph|bullet|numbered|table|page_break, level, text, rows}]}`. Excel `{sheets: [{name, header_row, rows: [[1, "text", "=A1*2", {"date": "2026-11-07"}, null]]}]}`. PowerPoint `{slides: [{layout: title|content|section|blank, title, text, bullets, notes}]}`.
