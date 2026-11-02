# Eval baseline: M1 (2026-11-02)

Client: M0 + M1 context packages. Server: M1 core (Anthropic, OpenAI-chat and Responses adapters). Models via OpenRouter. One attempt per task; total cost about $0.093.

| Model | Pass rate | Passed | Timeouts | Errors | Avg turns | Avg tokens | Input | Output | Cache | Cost (USD) | Avg time |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| claude-haiku | 100% | 7/7 | 0 | 0 | 4.7 | 38.7k | 80 | 13.7k | 256.9k | 0.0326 | 11.9s |
| gpt-luna | 100% | 7/7 | 0 | 0 | 6.1 | 31.3k | 44.0k | 7109 | 167.9k | 0.0248 | 19.9s |
| deepseek-flash | 100% | 7/7 | 0 | 0 | 6.3 | 40.0k | 74.3k | 12.0k | 193.5k | 0.0181 | 15.6s |
| qwen-flash | 100% | 7/7 | 0 | 0 | 7.0 | 42.7k | 137.3k | 12.0k | 149.8k | 0.0102 | 24.2s |
| glm-flash | 86% | 6/7 | 0 | 0 | 5.7 | 31.2k | 89.7k | 9627 | 118.9k | 0.0069 | 15.6s |

| Model | Task | # | Status | Turns | Detail |
|---|---|---:|---|---:|---|
| glm-flash | py-implement-from-docstring | 1 | fail | 7 | check.sh failed, see /Users/xlsft/Projects/xlsft/harness/evals/results/m1-baseline/runs/glm-flash/py-implement-from-docstring/1/check.log |
## Per task

| Task | Tags | claude-haiku | gpt-luna | deepseek-flash | qwen-flash | glm-flash |
|---|---|---:|---:|---:|---:|---:|
| go-fix-failing-test | bugfix | 1/1 (4.0 turns) | 1/1 (6.0 turns) | 1/1 (8.0 turns) | 1/1 (6.0 turns) | 1/1 (6.0 turns) |
| go-off-by-one-bug-report | bugfix | 1/1 (8.0 turns) | 1/1 (8.0 turns) | 1/1 (9.0 turns) | 1/1 (10.0 turns) | 1/1 (7.0 turns) |
| py-answer-question | read-only | 1/1 (3.0 turns) | 1/1 (3.0 turns) | 1/1 (4.0 turns) | 1/1 (3.0 turns) | 1/1 (4.0 turns) |
| py-implement-from-docstring | feature | 1/1 (4.0 turns) | 1/1 (8.0 turns) | 1/1 (7.0 turns) | 1/1 (6.0 turns) | 0/1 (7.0 turns) |
| shell-release-checksum | shell, docs | 1/1 (6.0 turns) | 1/1 (6.0 turns) | 1/1 (5.0 turns) | 1/1 (8.0 turns) | 1/1 (4.0 turns) |
| ts-implement-lru-cache | feature | 1/1 (4.0 turns) | 1/1 (5.0 turns) | 1/1 (6.0 turns) | 1/1 (10.0 turns) | 1/1 (7.0 turns) |
| ts-rename-across-files | refactor | 1/1 (4.0 turns) | 1/1 (7.0 turns) | 1/1 (5.0 turns) | 1/1 (6.0 turns) | 1/1 (5.0 turns) |

glm-flash failed `py-implement-from-docstring` (its slugify output did not match the hidden tests).
