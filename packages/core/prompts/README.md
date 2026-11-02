# Prompt files

The server builds the prompt bundle (`harness.prompts.v1.PromptService/GetBundle`) from this directory
(`HARNESS_PROMPTS_DIR`, default `./prompts`). Files are re-read on every request, so edits apply without a restart.
The bundle `version` is a content hash: it changes exactly when the resolved texts change.

| Path | Bundle field |
|---|---|
| `system/static/NN-<id>.md` | static system sections, ordered by file name; `NN-` is an optional sort prefix and is stripped from the id |
| `system/dynamic/NN-<id>.md` | dynamic sections, placed after the dynamic boundary marker; may contain `{{placeholders}}` |
| `tools/<Name>/description.md` + `tools/<Name>/schema.json` | `tools[<Name>]` (schema is JSON Schema draft-07) |
| `reminders/<id>.md` | `reminders[<id>]` |
| `compaction/instruction.md` | `compaction_instruction` |
| `title/instruction.md` | `title_instruction` |

Trailing whitespace of each file is trimmed. This README is not part of the bundle.

## Per-model overrides

`overrides/<glob>/` mirrors the layout above. `<glob>` is matched (case-insensitive, `*` and `?` wildcards)
against the model alias **and** the upstream model id, e.g. `overrides/gpt-*/`, `overrides/deepseek-*/`,
`overrides/claude-sonnet/`.

- Every matching override directory is applied on top of the base files, from least to most specific
  (fewer wildcards and longer literal text = more specific); an exact alias match wins last.
- A file in an override replaces the file with the same relative path, or adds it (a new section, tool or reminder).
- An **empty** file deletes the base file (e.g. an empty `tools/Grep/description.md` removes the Grep tool for that model).
