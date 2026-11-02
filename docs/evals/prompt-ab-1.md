# Prompt A/B: Bash job control, spec boundaries, output format (2026-11-02)

Weak models, 11 tasks × 2 attempts, via OpenRouter. Change: three additions to `tools/Bash/description.md` and `system/static/40-doing_tasks.md` (job control does not survive between Bash calls and `&` processes are untracked; check boundary cases of a spec yourself; follow user-specified output formats exactly).

`ts-options-object-refactor` is excluded from the comparison: its check falsely failed correct solutions in the "before" run (fixed in 9139e03).

| Model | Before (10 tasks) | After (10 tasks) | After, refactor task |
|---|---:|---:|---:|
| qwen-flash | 15/20 | 17/20 | 2/2 |
| glm-flash | 19/20 | 20/20 | 2/2 |

Targeted tasks: shell-background-server qwen 1/2 → 2/2; py-implement-from-docstring qwen 0/2 → 1/2, glm 1/2 → 2/2; go-count-critical-audits qwen 0/2 → 1/2.
