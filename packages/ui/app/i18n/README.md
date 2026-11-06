# Translating the web UI

English text in the code is the key: `tt('Save')`, `tt('Connect to {0}', [name])`. `catalog.json` lists every string; it is generated, so never edit it by hand.

To add a language:

1. Make `locales/<locale>.json`, a flat map from the English text to the translation. Keep `{0}`, `{1}`... in whatever place the sentence needs them. A string left out stays English.
2. Add a line for it to `PACKS` in `app/utils/i18n.ts`. It then shows up in Settings > Appearance.

When adding UI, wrap new text in `tt(...)`, or run `node scripts/i18n-extract.mjs --apply`, which does it and rewrites the catalog. `pnpm test` fails when the catalog is stale or a template holds plain text.

Not translated on purpose: the product's name, model and provider names, what comes from the server or a model, and the prompts.
