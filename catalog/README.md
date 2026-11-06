# The skills catalog

The plugin marketplace of xlsft\`s harness: a curated list, read by `harness-client plugin` like any other marketplace.

```bash
harness-client plugin marketplace add ./catalog
harness-client plugin marketplace list
harness-client plugin install engineering@harness-catalog
```

Installing fetches the plugin at its pinned commit into `HARNESS_HOME/plugins` and runs nothing. It runs once you approve it on this device.

## What gets listed

- **Every entry has a license we read.** `license` is its SPDX id, from a short list. `catalog.licenseFile` says which file at the pinned commit holds it. The check identifies the license from that text and compares the terms word for word, so a changed term fails it.
- **External entries are pinned by commit.** `source` names the repository and a full `sha`. A ref alone is refused.
- **External entries are referenced, not copied.** No upstream file is in this repository.
- **Origin is stated as a fact.** `catalog.upstream` and the description name the repository. This catalog is not made by, endorsed by or affiliated with the authors of those repositories, and no entry carries their logos.

`scripts/catalog/check.py` enforces this, and `.github/workflows/catalog.yml` runs it. To read the licenses by hand, run `scripts/catalog/check.py --verify-licenses`.

## Entry fields

The `catalog` object is ours. The client ignores it.

| Field | Meaning |
|---|---|
| `upstream` | The source repository, `owner/repo` |
| `licenseFile` | The license, as a path in that repository |
| `licenseNotInstalled` | The license is outside the plugin's folder, so an install doesn't carry it (see below) |
| `licenseExtraTextSha256` | A person read the text around the license and found it changes no terms. Different text fails the check again |
| `additionalLicenses` | Bundled files under another listed license, each read at the pin |
| `note` | What a reader should know about the entry's license or content |

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

- **`canvas-design`** bundles fonts under the SIL Open Font License, with their license files beside them.
- **`slack-gif-creator`** lists Pillow, imageio, imageio-ffmpeg and NumPy in `requirements.txt`. They are installed separately and not bundled. imageio-ffmpeg brings FFmpeg, which upstream's notices list under the GPL.
- **`anthropics/skills`** keeps its notices in `THIRD_PARTY_NOTICES.md` at the repository's root.

An install copies only the plugin's folder. A license or notice at the root of the upstream repository stays there, which is what `licenseNotInstalled` records.
