# harness visual identity

The layout follows Claude Code. The identity is our own: logo, colors, loaders and glyphs. Never reuse Claude Code's spinner glyphs or verbs, its accent colors, its logo or its banner art.

Live preview: the "harness CLI identity" artifact. Logo files are in [`docs/assets`](../assets).

## Logo

- **Mark:** a chevron plus two parallelogram bars, filled with `#2B7FFF`. Source: [`logo.svg`](../assets/logo.svg).
- **Animated mark:** [`logo-animated.svg`](../assets/logo-animated.svg). The pieces assemble once (chevron from the left, top bar from the top right, bottom bar from the right, 22 px travel, ~0.9 s), then a blue-400 rail flash loops through them every 1.2 s. Pure SVG + CSS; static under `prefers-reduced-motion`.
- **Wordmark:** "xlsft's harness". Use [`logo-full-dark.svg`](../assets/logo-full-dark.svg) (white text) on dark backgrounds and [`logo-full-light.svg`](../assets/logo-full-light.svg) (zinc-950 text) on light ones.
- **Terminal mark:** the SVG polygons rasterized into half-block cells (`▀ ▄ █`), drawn in the accent color.

**18×6 (welcome screen, default):**

```
████▄      ▄████▀
▀█████▄  ▄████▀
  ▀█████▄███▀
  ▄█████▀ ▄███████
▄█████▀ ▄████████▀
████▀ ▄████████▀
```

The product name, version, model/account, cwd/branch and a hint line sit to the right of rows 2–5. If the terminal is narrower than 60 columns, use the 12×4 variant:

```
███▄   ▄██▀
 ▀███▄██▀
 ▄███▀▄█████
███▀▄█████▀
```

## Color

Tailwind CSS v4 palette only:
- **zinc** — monochrome;
- **blue** — accent; the logo color `#2B7FFF` is blue-500;
- **emerald / amber / rose** — success, warning and error.

Shades are picked per background: lighter shades on dark terminals, darker shades on light ones.

| Role | Dark terminal | Light terminal | ANSI-256 | ANSI-16 | `NO_COLOR` |
|---|---|---|---|---|---|
| Accent: brand, prompt `❯`, tool bullet, focus | blue-500 `#2B7FFF` | blue-600 `#155DFC` | 33 | blue (34) | bold |
| Accent highlight: loader head, selection | blue-300 `#8EC5FF` | blue-700 `#1447E6` | 117 | bright blue (94) | bold + reverse |
| Accent low: loader tail | blue-600 `#155DFC` | blue-400 `#51A2FF` | 26 | blue (34) | dim |
| Primary text | terminal default | terminal default | default | default | normal |
| Secondary text | zinc-400 `#A1A1AA` | zinc-600 `#52525B` | 248 | default | normal |
| Dim text: hints, metadata | zinc-500 `#71717A` | zinc-500 `#71717A` | 243 | bright black (90) | dim |
| Lines, borders, empty loader cells | zinc-700 `#3F3F46` | zinc-300 `#D4D4D8` | 238 | bright black (90) | dim |
| Success | emerald-400 `#00D492` | emerald-600 `#009966` | 42 | green (32) | bold |
| Warning | amber-400 `#FFB900` | amber-600 `#E17100` | 214 | yellow (33) | bold |
| Error | rose-400 `#FF637E` | rose-600 `#EC003F` | 204 | red (31) | bold |
| Diff add, background / text | emerald-950 `#002C22` / emerald-300 `#5EE9B5` | emerald-100 `#D0FAE5` / emerald-800 `#006045` | 22 / 79 | green text (32), no background | `+` prefix |
| Diff remove, background / text | rose-950 `#4D0218` / rose-300 `#FFA1AD` | rose-100 `#FFE4E6` / rose-800 `#A50036` | 52 / 217 | red text (31), no background | `-` prefix |

Rules:
- **Color depth** is detected from `COLORTERM` (`truecolor` / `24bit`), `TERM` and the terminfo color count. `NO_COLOR` always wins. `HARNESS_COLOR=truecolor|256|16|none` forces a level.
- **Light or dark background** is detected via OSC 11 or `COLORFGBG`; if neither works, assume dark. `HARNESS_THEME=dark|light` forces it.
- **Status colors carry meaning only** (success, warning, error, diff). Never use them as decoration. The accent is the only brand color.

## Loaders

### Rail — the main "working" indicator

A chevron followed by four parallelogram cells. A bright head moves left to right, followed by a fading tail. This is the logo mark in motion.

```
❯ ▰▱▱▱   ❯ ▰▰▱▱   ❯ ▰▰▰▱   ❯ ▱▰▰▰   ❯ ▱▱▰▰   ❯ ▱▱▱▰   ❯ ▱▱▱▱
```

- **Frames:** 7 per cycle (4 cells + 3 tail steps), 120 ms each.
- **Colors:** head blue-300, then blue-500, then blue-600; empty cells zinc-700. The chevron is always blue-500.
- **Line layout:** `⟨rail⟩  ⟨status⟩ · ⟨elapsed⟩ · ↓ ⟨tokens⟩ · esc to interrupt`.
- **Status words** are plain and descriptive: `Thinking`, `Writing`, `Reading`, `Running <tool>`, `Waiting for approval`, `Compacting`, `Retrying (2/10)`. No whimsical rotating verbs.

### Pulse — a running tool call

One cell: the tool's bullet `❯` cycles through blue-600 → blue-500 → blue-400 → blue-300 → blue-400 → blue-500, 120 ms per step. Once the call finishes, the bullet settles to `▰`, colored by the result: blue-500 for success, rose-400 for an error.

### Fallbacks

| Terminal | Rail | Pulse |
|---|---|---|
| 256 colors | same frames, ANSI-256 colors from the table | same |
| 16 colors | head bright blue, tail blue, empty bright black | alternates `❯` and `›` |
| No color (`NO_COLOR`) | head bold, tail normal, empty dim | alternates `❯` and `›` |
| No Unicode (`LANG` without UTF-8, or `HARNESS_ASCII=1`) | `>    ` `>/   ` `>//  ` `> // ` `>  //` `>   /` | alternates `>` and ` ` |

`prefers-reduced-motion` has no terminal equivalent. Use `HARNESS_REDUCED_MOTION=1`: the rail stops at `❯ ▰▰▰▰` and only the elapsed time updates.

## Glyphs

| Element | Glyph | Color |
|---|---|---|
| Input prompt | `❯` | accent |
| Assistant message / tool call bullet | `▰` | accent (success), rose (error), emerald (final "done" line) |
| Tool result connector | `└` | lines |
| Collapsed output hint | `… +N lines (ctrl+o to expand)` | dim |
| Selected menu item | `❯` + accent-highlight text | accent |
| Permission box | rounded box (`╭ ╮ ╰ ╯`), title in bold accent | lines for the border |
| Mode indicator (accept edits) | `⏵⏵` | accent |
| Queued message | text plus `(queued)` | dim |

## Web and desktop UI

The same tokens apply to `packages/ui` through Nuxt UI's theme:
- `primary: 'blue'`, `neutral: 'zinc'`, `success: 'emerald'`, `warning: 'amber'`, `error: 'rose'`;
- the full UI design will be provided separately.
