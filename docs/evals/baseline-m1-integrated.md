# Eval baseline: M1 integrated (2026-11-02)

Client: M1 (permissions, plan mode, tasks, checkpoints, compaction) with settings, memory and sync wired in. Server: M1 core. 11 tasks (4 harder ones added: long file, repo-wide count, multi-file refactor, background server), one attempt each, models via OpenRouter, 4 runs in parallel. Total cost about $0.24.

| Model | Pass rate | Passed | Timeouts | Errors | Avg turns | Avg tokens | Input | Output | Cache | Cost (USD) | Avg time |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| claude-haiku | 100% | 11/11 | 0 | 0 | 4.6 | 59.8k | 126 | 20.3k | 636.9k | 0.0738 | 21.3s |
| gpt-luna | 100% | 11/11 | 0 | 0 | 7.8 | 59.3k | 107.9k | 15.7k | 528.8k | 0.0715 | 1m20.9s |
| deepseek-flash | 100% | 11/11 | 0 | 0 | 7.1 | 65.6k | 155.1k | 25.3k | 540.9k | 0.0440 | 35.3s |
| qwen-flash | 73% | 8/11 | 1 | 0 | 6.2 | 63.6k | 317.1k | 17.3k | 365.1k | 0.0227 | 1m8.3s |
| glm-flash | 91% | 10/11 | 0 | 0 | 7.6 | 87.2k | 170.7k | 29.8k | 758.7k | 0.0297 | 53s |

## Per task

| Task | Tags | claude-haiku | gpt-luna | deepseek-flash | qwen-flash | glm-flash |
|---|---|---:|---:|---:|---:|---:|
| go-count-critical-audits | read-only, search | 1/1 (4.0 turns) | 1/1 (4.0 turns) | 1/1 (14.0 turns) | 0/1 (0.0 turns) | 1/1 (20.0 turns) |
| go-fix-failing-test | bugfix | 1/1 (4.0 turns) | 1/1 (8.0 turns) | 1/1 (7.0 turns) | 1/1 (8.0 turns) | 1/1 (5.0 turns) |
| go-long-file-needle | bugfix, long-file | 1/1 (5.0 turns) | 1/1 (10.0 turns) | 1/1 (8.0 turns) | 1/1 (8.0 turns) | 1/1 (7.0 turns) |
| go-off-by-one-bug-report | bugfix | 1/1 (6.0 turns) | 1/1 (13.0 turns) | 1/1 (7.0 turns) | 1/1 (7.0 turns) | 1/1 (8.0 turns) |
| py-answer-question | read-only | 1/1 (3.0 turns) | 1/1 (5.0 turns) | 1/1 (4.0 turns) | 1/1 (3.0 turns) | 1/1 (4.0 turns) |
| py-implement-from-docstring | feature | 1/1 (4.0 turns) | 1/1 (6.0 turns) | 1/1 (7.0 turns) | 0/1 (6.0 turns) | 0/1 (7.0 turns) |
| shell-background-server | shell, background | 1/1 (7.0 turns) | 1/1 (8.0 turns) | 1/1 (9.0 turns) | 0/1 (8.0 turns) | 1/1 (9.0 turns) |
| shell-release-checksum | shell, docs | 1/1 (4.0 turns) | 1/1 (5.0 turns) | 1/1 (4.0 turns) | 1/1 (4.0 turns) | 1/1 (5.0 turns) |
| ts-implement-lru-cache | feature | 1/1 (4.0 turns) | 1/1 (5.0 turns) | 1/1 (6.0 turns) | 1/1 (5.0 turns) | 1/1 (8.0 turns) |
| ts-options-object-refactor | refactor, multi-file | 1/1 (5.0 turns) | 1/1 (10.0 turns) | 1/1 (6.0 turns) | 1/1 (8.0 turns) | 1/1 (6.0 turns) |
| ts-rename-across-files | refactor | 1/1 (5.0 turns) | 1/1 (12.0 turns) | 1/1 (6.0 turns) | 1/1 (5.0 turns) | 1/1 (5.0 turns) |

## Failures

- qwen-flash `go-count-critical-audits`: timed out at 6 min under parallel load. A solo rerun finished in 3 min with the right count (335) but without the required `ANSWER:` line.
- qwen-flash `shell-background-server`: started the server with `&` in the shell (outside harness background tasks) and never stopped it.
- qwen-flash and glm-flash `py-implement-from-docstring`: `slugify` edge cases fail the hidden tests.
