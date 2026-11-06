# The skills catalog

The plugin marketplace of xlsft\`s harness: a curated list, read by `harness-client plugin` like any other marketplace.

```bash
harness-client plugin marketplace add ./catalog
harness-client plugin marketplace list
harness-client plugin install engineering@harness-catalog
```

Installing fetches the plugin at its pinned commit into `HARNESS_HOME/plugins` and runs nothing. It runs once you approve it on this device.

## What gets listed

- **Every entry has a license we read.** `license` is its SPDX id, from a short list. `licenseFile` says which file holds it. The check identifies the license from that text and compares the terms word for word, so a changed term fails it.
- **External entries are pinned by commit.** `source` names the repository and a full `sha`. A ref alone is refused.
- **External entries are referenced, not copied.** The only upstream files here belong to a fork, kept under `plugins/` with their license.
- **An install fetches only what the entry names.** That is the plugin's folder and its `licenseFile`, at the pin. A server that can't send part of a repository is refused rather than downloaded from in full.
- **Origin is stated as a fact.** `catalog.upstream` and the description name the repository. This catalog is not made by, endorsed by or affiliated with the authors of those repositories, and no entry carries their logos.

`scripts/catalog/check.py` enforces this, and `.github/workflows/catalog.yml` runs it. To read the licenses by hand, run `scripts/catalog/check.py --verify-licenses`.

## Entry fields

`licenseFile` sits beside `license`, which stays a plain string as the format has it. It is a path from the root of the entry's repository, or of this catalog for a bundled plugin. The client copies a license from outside the plugin's folder into the plugin's root, at the pinned commit, so it stays with the plugin after install. A tool that doesn't know the field passes over it.

The `catalog` object is ours. The client ignores it.

| Field | Meaning |
|---|---|
| `upstream` | The source repository, `owner/repo` |
| `licenseExtraTextSha256` | A person read the text around the license and found it changes no terms. Different text fails the check again |
| `additionalLicenses` | Bundled files under another listed license. `files` are read at the pin. `covers` lists files whose license was read at the `sources` named, each pinned |
| `fork` | Where a fork comes from (see below) |
| `note` | What a reader should know about the entry's license or content |

## Forks

Apache-2.0 allows changing what it covers, on conditions. A fork lives in `plugins/<name>/` and keeps the upstream's license file unchanged. `catalog.fork` records `upstream`, `sha`, `path` and `modifiedFiles`.

Every changed file says so near its top, starting `Modified from <owner/repo>` and naming the commit (Apache-2.0, section 4). The check refuses a fork without these, a change that isn't declared, a declared change that isn't one, and any remaining mention of the upstream's vendor or product outside the license and the origin.

| Fork | What changed |
|---|---|
| `algorithmic-art` | The original brand colors and fonts are gone from the templates, and its name is gone from the instructions. The template has a neutral default, and the instructions ask for the design to be described in plain terms and chosen to fit the piece |

## Decisions

- **`licenseFile` is its own field, not `license.file`.** `license` is a string in the format. An object there would stop this client and other tools from parsing the entry.
- **The owner kept `canvas-design` and `algorithmic-art`.** `canvas-design` stays unforked because all its fonts were confirmed open. `algorithmic-art` is a fork because its template kept its vendor's brand.
- **The fork has no link to the app's theme.** The owner decided against it, so there is no `postMessage` and no viewer change. The template's neutral default is free to change.
- **The owner deferred the `documents` plugin (`docx`, `pdf`, `pptx`, `xlsx`).** It will be designed separately. The clean-room rule stays: don't read the versions in `anthropics/skills`.
- **The file listing of a partial fetch is accepted.** A `git-subdir` install receives the repository's folder and file names as part of the commit, but no file contents outside the entry.

## Refused or left out

The check refuses these from `anthropics/skills`, before anything is fetched. Please don't read them to compare, either.

| Skill | Why |
|---|---|
| `docx`, `pdf`, `pptx`, `xlsx` | Their license forbids copying and derivative works |
| `doc-coauthoring` | It has no license file |
| `claude-api`, `web-artifacts-builder`, `academy-guide` | They only make sense on their vendor's products |
| `brand-guidelines` | It is their vendor's brand |

From `anthropics/knowledge-work-plugins`, these are left out by choice, and the check does not enforce it:

| Plugin | Why |
|---|---|
| `partner-built/*` | Not reviewed one by one |
| `cowork-plugin-management` | It manages plugins for its vendor's own desktop product |
| `pdf-viewer` | It is a tool rather than a role plugin |
| `small-business` | It is not a role plugin. Its install steps name its vendor's product, and it brings 35 MCP servers, payment services among them |

## Third-party notices

- **`canvas-design`** bundles 54 font files, all under the SIL Open Font License. Each records that license in its own name table. 48 have a license file beside them. For the other six, `IBMPlexSerif-*` and `InstrumentSerif-*`, the entry records the license read at the family's own repository and at Google Fonts. IBM Plex keeps the Reserved Font Name "Plex".
- **`slack-gif-creator`** lists Pillow, imageio, imageio-ffmpeg and NumPy in `requirements.txt`. They are installed separately and not bundled. imageio-ffmpeg brings FFmpeg, which upstream's notices list under the GPL.
- **`anthropics/skills`** keeps its notices in `THIRD_PARTY_NOTICES.md` at the repository's root.

An install copies the plugin's folder and its `licenseFile`. Other notices at the root of an upstream repository stay there.
