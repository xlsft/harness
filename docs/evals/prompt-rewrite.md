# Prompt bundle rewrite — eval comparison

The prompt bundle was rewritten from the behavior spec in [prompt-requirements.md](../design/prompt-requirements.md): system sections, tool descriptions, reminders, compaction, title, the auto-mode classifier, and five built-in output styles. The new bundle is about 55% longer.

Suite: 11 tasks × 2 repeats, models via OpenRouter, local dev stack. "Before" is the original bundle, "after" adds the three A/B fixes from [prompt-ab-1.md](prompt-ab-1.md), "rewrite" is the new bundle.

| Model | Before | After | Rewrite |
|---|---:|---:|---:|
| qwen-flash | 15/22 | 19/22 | 19/22 |
| glm-flash | 19/22 | 22/22 | 21/22 |
| claude-haiku | — | — | 22/22 |

Rewrite failures: qwen misses the truncation rule in `py-implement-from-docstring` (2/2, also failed before) and times out once on `go-count-critical-audits`; glm hits the 25-turn limit once on `go-count-critical-audits`. Both are within run-to-run noise of "after".

Cost per run is roughly unchanged (qwen $0.078 vs $0.067, glm $0.084 vs $0.055): the longer prompt is mostly served from cache.

Decision: merged. Pass rates hold, and the bundle is now a clean-room text that also carries output styles.
