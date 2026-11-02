# harness

Харнесс на замену Claude Code: те же инструменты, тот же UX, любые модели через OpenAI- и Anthropic-совместимые эндпоинты.

| Пакет | Что | Стек |
|---|---|---|
| [`packages/core`](packages/core) | Сервер: auth, LLM-gateway, промпты, данные пользователей, MCP, relay | Nitro v3, TypeScript |
| [`packages/client`](packages/client) | Универсальный клиент: агентный цикл и исполнение инструментов | Go |
| [`packages/cli`](packages/cli) | Терминальный фронтенд | Go, charm.land |
| [`packages/ui`](packages/ui) | Фронтенд для desktop и mobile | Nuxt 4, Nuxt UI |

- План: [docs/PLAN.md](docs/PLAN.md)
- Протоколы: [docs/protocol](docs/protocol)
